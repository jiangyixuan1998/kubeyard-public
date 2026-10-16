package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"kubeyard/internal/model"
)

type EngineMode string

const (
	EngineLegacy  EngineMode = "legacy"
	EngineShadow  EngineMode = "shadow"
	EngineEnforce EngineMode = "enforce"
)

const legacyProfilePrefix = "legacy-"

var (
	engineValue atomic.Value // EngineMode
	syncMu      sync.Mutex
	checks      atomic.Uint64
	mismatches  atomic.Uint64
	errors      atomic.Uint64
)

func init() { engineValue.Store(EngineShadow) }

func ParseEngine(v string) EngineMode {
	switch EngineMode(strings.ToLower(strings.TrimSpace(v))) {
	case EngineLegacy:
		return EngineLegacy
	case EngineEnforce:
		return EngineEnforce
	default:
		return EngineShadow
	}
}

func SetEngine(v string) { engineValue.Store(ParseEngine(v)) }

func Engine() EngineMode { return engineValue.Load().(EngineMode) }

type Stats struct {
	Engine     string `json:"engine"`
	Checks     uint64 `json:"checks"`
	Mismatches uint64 `json:"mismatches"`
	Errors     uint64 `json:"errors"`
}

func GetStats() Stats {
	return Stats{
		Engine:     string(Engine()),
		Checks:     checks.Load(),
		Mismatches: mismatches.Load(),
		Errors:     errors.Load(),
	}
}

// CompareAllowed puts the new decision beside the legacy one. Shadow never changes
// production behavior; enforce uses the new result only after the caller's sync passed.
func CompareAllowed(kind, detail string, legacy bool, next func() (bool, error)) bool {
	mode := Engine()
	if mode == EngineLegacy {
		return legacy
	}
	nextValue, err := next()
	checks.Add(1)
	if err != nil {
		errors.Add(1)
		log.Printf("[WARN] authz %s engine error: %s: %v", mode, detail, err)
		return false
	}
	if nextValue != legacy {
		mismatches.Add(1)
		log.Printf("[WARN] authz %s mismatch: %s legacy=%t next=%t", mode, detail, legacy, nextValue)
	}
	if mode == EngineEnforce {
		return nextValue
	}
	return legacy
}

// CompareActions keeps nil and empty distinct: nil means unrestricted, empty denies.
func CompareActions(kind, detail string, legacy []string, next func() ([]string, error)) []string {
	mode := Engine()
	if mode == EngineLegacy {
		return legacy
	}
	nextValue, err := next()
	checks.Add(1)
	if err != nil {
		errors.Add(1)
		log.Printf("[WARN] authz %s update-actions error: %s: %v", mode, detail, err)
		if mode == EngineEnforce {
			return []string{}
		}
		return legacy
	}
	if !sameActions(legacy, nextValue) {
		mismatches.Add(1)
		log.Printf("[WARN] authz %s update-actions mismatch: %s legacy=%v next=%v", mode, detail, legacy, nextValue)
	}
	if mode == EngineEnforce {
		return nextValue
	}
	return legacy
}

func sameActions(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type capability struct {
	Resources string
	Verbs     string
	Actions   string
}

type capabilityRow struct {
	Resources string
	Verbs     string
	Actions   string
	Namespace string
}

// SyncFromLegacy rebuilds Profile/Binding rows from the current authoritative tables.
// The full reconcile keeps the shadow model usable after every existing write path.
func SyncFromLegacy(db *gorm.DB) error {
	syncMu.Lock()
	defer syncMu.Unlock()

	return db.Transaction(func(tx *gorm.DB) error {
		var grants []model.UserGrant
		if err := tx.Find(&grants).Error; err != nil {
			return err
		}
		var rules []model.GrantGroupRule
		if err := tx.Find(&rules).Error; err != nil {
			return err
		}

		type profileSeed struct {
			model.AccessProfile
			capability capability
		}
		seeds := map[string]profileSeed{}
		addSeed := func(c capability) uint {
			key := profileKey(c)
			seed, ok := seeds[key]
			if !ok {
				sum := sha256.Sum256([]byte(key))
				seed = profileSeed{
					AccessProfile: model.AccessProfile{
						Name:        legacyProfilePrefix + hex.EncodeToString(sum[:8]),
						Resources:   c.Resources,
						Verbs:       c.Verbs,
						Actions:     c.Actions,
						Description: "Migrated from legacy grants",
					},
					capability: c,
				}
				seeds[key] = seed
			}
			return seed.ID
		}

		for _, g := range grants {
			addSeed(capability{Resources: g.Resource, Verbs: g.Verb, Actions: g.Actions})
		}
		for _, r := range rules {
			addSeed(capability{Resources: r.Resource, Verbs: r.Verb, Actions: r.Actions})
		}

		var existing []model.AccessProfile
		if err := tx.Where("name LIKE ?", legacyProfilePrefix+"%").Find(&existing).Error; err != nil {
			return err
		}
		profileIDs := map[string]uint{}
		seenCapability := map[string]bool{}
		for _, p := range existing {
			c := capability{Resources: p.Resources, Verbs: p.Verbs, Actions: p.Actions}
			key := profileKey(c)
			if seenCapability[key] {
				return fmt.Errorf("权限能力存在重复 Profile: %s / %s", p.Name, key)
			}
			seenCapability[key] = true
			expected := profileName(key)
			if p.Name != expected {
				return fmt.Errorf("legacy Profile 名称与能力不匹配: %s", p.Name)
			}
			profileIDs[key] = p.ID
		}
		for key, seed := range seeds {
			if id, ok := profileIDs[key]; ok {
				seed.ID = id
				seeds[key] = seed
				continue
			}
			row := seed.AccessProfile
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			seed.ID = row.ID
			seeds[key] = seed
			profileIDs[key] = row.ID
		}

		type bindingSeed struct {
			model.AccessBinding
		}
		wanted := map[string]bindingSeed{}
		bindingKey := func(profileID, userID, groupID, clusterID uint, namespace string, expiresAt *time.Time) string {
			expiry := int64(-1)
			if expiresAt != nil {
				expiry = expiresAt.UnixNano()
			}
			tuple := fmt.Sprintf("%d|%d|%d|%d|%s|%d", profileID, userID, groupID, clusterID, namespace, expiry)
			sum := sha256.Sum256([]byte(tuple))
			return hex.EncodeToString(sum[:])
		}
		addBinding := func(profileID, userID, groupID, clusterID uint, namespace string, expiresAt *time.Time, createdBy uint) {
			dedupKey := bindingKey(profileID, userID, groupID, clusterID, namespace, expiresAt)
			row := model.AccessBinding{
				DedupKey:  dedupKey,
				ProfileID: profileID,
				UserID:    userID,
				GroupID:   groupID,
				ClusterID: clusterID,
				Namespace: namespace,
				ExpiresAt: expiresAt,
				CreatedBy: createdBy,
			}
			wanted[bindingKey(profileID, userID, groupID, clusterID, namespace, expiresAt)] = bindingSeed{row}
		}
		for _, g := range grants {
			id := profileIDs[profileKey(capability{Resources: g.Resource, Verbs: g.Verb, Actions: g.Actions})]
			addBinding(id, g.UserID, 0, g.ClusterID, g.Namespace, g.ExpiresAt, g.CreatedBy)
		}
		for _, r := range rules {
			id := profileIDs[profileKey(capability{Resources: r.Resource, Verbs: r.Verb, Actions: r.Actions})]
			addBinding(id, 0, r.GroupID, r.ClusterID, r.Namespace, nil, 0)
		}

		var current []model.AccessBinding
		if err := tx.Find(&current).Error; err != nil {
			return err
		}
		currentIDs := map[string]uint{}
		for _, row := range current {
			currentIDs[bindingKey(row.ProfileID, row.UserID, row.GroupID, row.ClusterID, row.Namespace, row.ExpiresAt)] = row.ID
		}
		for key, seed := range wanted {
			if id, ok := currentIDs[key]; ok {
				seed.ID = id
				wanted[key] = seed
				continue
			}
			row := seed.AccessBinding
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		for key, id := range currentIDs {
			if _, ok := wanted[key]; !ok {
				if err := tx.Delete(&model.AccessBinding{}, id).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func profileKey(c capability) string {
	return strings.Join([]string{c.Resources, c.Verbs, c.Actions}, "\x00")
}

func profileName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return legacyProfilePrefix + hex.EncodeToString(sum[:8])
}

// Authorize mirrors middleware.Allowed exactly, including its cluster-level
// namespace rule: an empty requested namespace can only be granted by "*" or "".
func Authorize(db *gorm.DB, userID uint, clusterName, namespace, verb, resource string) (bool, error) {
	var cl model.Cluster
	if err := db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		return false, nil
	}
	var u model.User
	if err := db.First(&u, userID).Error; err == nil && u.Expired() {
		return false, nil
	}
	rows, err := userCapabilities(db, userID, cl.ID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if entryMatch(row.Resources, resource) && entryMatch(row.Verbs, verb) && entryMatch(row.Namespace, namespace) {
			return true, nil
		}
	}
	return false, nil
}

// UpdateActions mirrors middleware.updateActionsOf; presetKnown is supplied by the
// middleware action catalog so custom presets remain the single source of field names.
func UpdateActions(db *gorm.DB, userID uint, clusterName, namespace, resource string, presetKnown bool) ([]string, error) {
	var cl model.Cluster
	if err := db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		return []string{}, nil
	}
	var u model.User
	if err := db.First(&u, userID).Error; err == nil && u.Expired() {
		return []string{}, nil
	}
	rows, err := userCapabilities(db, userID, cl.ID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, row := range rows {
		if !entryMatch(row.Resources, resource) || !entryMatch(row.Verbs, "update") {
			continue
		}
		for _, action := range strings.Split(row.Actions, ",") {
			action = strings.TrimSpace(action)
			if action == "" {
				continue
			}
			if action == "*" {
				return nil, nil
			}
			out = append(out, action)
		}
	}
	if !presetKnown {
		for _, action := range out {
			if action == "update" {
				return nil, nil
			}
		}
	}
	return out, nil
}

func userCapabilities(db *gorm.DB, userID, clusterID uint) ([]capabilityRow, error) {
	now := time.Now()
	var rows []capabilityRow
	err := db.Table("access_profiles AS ap").
		Select("ap.resources AS resources, ap.verbs AS verbs, ap.actions AS actions, ab.namespace AS namespace").
		Joins("JOIN access_bindings AS ab ON ab.profile_id = ap.id").
		Where("ab.user_id = ? AND ab.cluster_id = ? AND (ab.expires_at IS NULL OR ab.expires_at > ?)", userID, clusterID, now).
		Order("ab.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	var memberships []model.UserGroup
	if err := db.Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", userID, now).Find(&memberships).Error; err != nil {
		return nil, err
	}
	if len(memberships) == 0 {
		return rows, nil
	}
	groupIDs := make([]uint, 0, len(memberships))
	for _, m := range memberships {
		groupIDs = append(groupIDs, m.GroupID)
	}
	var groupRows []capabilityRow
	err = db.Table("access_profiles AS ap").
		Select("ap.resources AS resources, ap.verbs AS verbs, ap.actions AS actions, ab.namespace AS namespace").
		Joins("JOIN access_bindings AS ab ON ab.profile_id = ap.id").
		Where("ab.group_id IN ? AND ab.cluster_id = ?", groupIDs, clusterID).
		Order("ab.id ASC").
		Scan(&groupRows).Error
	if err != nil {
		return nil, err
	}
	return append(rows, groupRows...), nil
}

// entryMatch is intentionally copied from middleware.entryMatch to avoid importing
// the request middleware and to make semantic drift visible in parity tests.
func entryMatch(granted, want string) bool {
	granted = strings.TrimSpace(granted)
	if granted == "*" || granted == "" {
		return true
	}
	if want == "" || want == "*" {
		return false
	}
	for _, item := range strings.Split(granted, ",") {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}
