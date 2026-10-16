<template>
  <div class="res-page" v-loading="loading">
    <!-- 左侧资源导航 -->
    <el-card shadow="never" class="nav-card" body-style="padding: 0">
      <el-menu ref="resourceMenu" :default-active="activeKey" :default-openeds="openedGroups" @select="onNavSelect" @open="onGroupOpen" @close="onGroupClose" class="nav-menu">
        <template v-for="g in filteredNav" :key="g.group">
          <el-sub-menu :index="g.group">
            <template #title>{{ g.group }}</template>
            <el-menu-item v-for="it in g.items" :key="`${it.group}|${it.version}|${it.resource}`" :index="`${it.group}|${it.version}|${it.resource}`">
              {{ it.label }}
            </el-menu-item>
          </el-sub-menu>
        </template>
      </el-menu>
    </el-card>

    <!-- 右侧主区 -->
    <div class="res-main">
      <el-card shadow="never">
        <div class="toolbar">
          <el-select v-model="cluster" placeholder="集群" style="width: 200px" filterable @change="onClusterChange">
            <el-option v-for="c in clusters" :key="c.name" :label="clusterLabel(c)" :value="c.name">
              <span>{{ clusterLabel(c) }}</span>
              <el-tag size="small" :type="statusColor(c.status)" style="margin-left: 8px">{{ c.status }}</el-tag>
            </el-option>
          </el-select>
          <el-select v-if="isNamespaced" v-model="namespace" placeholder="命名空间" style="width: 220px" clearable filterable @change="onNamespaceChange">
            <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
          </el-select>
          <el-input v-model="search" :placeholder="cur.resource === 'pods' ? '搜索名称 / Pod IP / 节点 IP / 节点名' : '按名称搜索'" style="width: 260px" clearable :prefix-icon="Search" />
          <el-tooltip v-if="!loading && (items.length || search)" placement="bottom"
            :content="truncated ? '结果超过单次拉取上限（每命名空间 500 条），仅显示前一部分；搜索不受此限（全量检索），或指定命名空间缩小范围' : '当前筛选条件下命中的资源总数'">
            <span class="res-count">共 {{ items.length }} 项<template v-if="truncated">+</template></span>
          </el-tooltip>
          <span class="current-res">{{ currentLabel }}</span>
          <template v-if="supportsBatch">
            <el-button v-if="canBatchStop" size="small" type="danger" plain :disabled="!selectedRows.length" @click="batchStop">批量停止</el-button>
            <el-button v-if="cur.resource !== 'pods' && actionAllowed(authResKey, 'restart', namespace)" size="small" type="warning" plain :disabled="!selectedRows.length" @click="batchRestart">批量重启</el-button>
            <el-button v-if="verbAllowed(authResKey, 'delete', namespace)" size="small" type="danger" :disabled="!selectedRows.length" @click="batchDelete">批量删除</el-button>
            <span v-if="selectedRows.length" class="dim" style="font-size: 12px">已选 {{ selectedRows.length }} 项</span>
          </template>
          <div style="flex: 1"></div>
          <el-tooltip content="自动刷新列表（30 秒，切换页面/筛选后仍生效）" placement="bottom">
            <el-switch v-model="autoRefresh" active-text="自动" />
          </el-tooltip>
          <el-button :icon="Refresh" circle :loading="loading" @click="load" title="刷新" />
          <el-button v-if="canOpenFormDeploy" type="primary" @click="openFormDeploy"><el-icon><MagicStick /></el-icon>白屏部署</el-button>
          <el-button v-if="canCreateHpa" type="primary" @click="openHpaDeploy"><el-icon><MagicStick /></el-icon>白屏部署</el-button>
          <el-button v-if="canCreateQuota" type="primary" @click="openQuotaDeploy"><el-icon><MagicStick /></el-icon>白屏部署</el-button>
          <el-button v-if="canCreateCur" type="primary" @click="openCreateCur"><el-icon><MagicStick /></el-icon>白屏创建</el-button>
          <el-button v-if="canDeploy" @click="deployDlg = true"><el-icon><Upload /></el-icon>YAML 部署</el-button>
        </div>

        <el-table ref="tableRef" :data="items" stripe border height="calc(100vh - 260px)" :default-sort="{ prop: 'ctime', order: 'descending' }" @selection-change="onSelectionChange">
          <el-table-column v-if="supportsBatch" type="selection" width="42" />
          <!-- 名称列宽按资源区分：nodes 主显示为 IP + 原主机名副行，其余资源单行名 -->
          <el-table-column prop="metadata.name" label="名称" :min-width="cur.resource === 'nodes' ? 170 : 230" show-overflow-tooltip sortable :sort-method="sortName">
            <template #default="{ row }">
              <!-- 节点命名不规范（有主机名有 IP）：统一显示内网 IP，原 metadata.name 缩小放副行（相同则不重复显示） -->
              <template v-if="cur.resource === 'nodes' && nodeIp(row)">
                <div class="node-name-cell">
                  <el-link type="primary" @click="openDetail(row)">{{ nodeIp(row) }}</el-link>
                  <span v-if="nodeIp(row) !== row.metadata.name" class="dim node-host" :title="row.metadata.name">{{ row.metadata.name }}</span>
                </div>
              </template>
              <el-link v-else type="primary" @click="openDetail(row)">{{ row.metadata.name }}</el-link>
            </template>
          </el-table-column>
          <el-table-column v-if="isNamespaced" label="命名空间" :width="cur.resource === 'pods' ? 150 : 145" show-overflow-tooltip sortable :sort-method="sortNS">
            <template #default="{ row }">{{ nsAliasLabel(row.metadata.namespace) }}</template>
          </el-table-column>
          <!-- 关键信息列宽按资源区分：nodes 要放 Ready + 角色标签 + kubelet 版本三段；pods 状态 tag + 呼吸灯 + 1/1 · 重启 0，170 保单行不换行 -->
          <el-table-column label="关键信息" :min-width="cur.resource === 'nodes' ? 270 : (cur.resource === 'pods' ? 170 : 130)" show-overflow-tooltip sortable :sort-method="sortKeyInfo">
            <template #default="{ row }">
              <template v-if="cur.resource === 'pods'">
                <!-- 容器状态点：一容器一枚（绿=运行就绪呼吸 / 红=异常涟漪 / 灰=已退出或初始化），悬浮看容器名与状态；
                     尚无容器状态（Pending 等）时回退整 Pod 呼吸灯 -->
                <span v-if="cdotList(row).length" class="cdots">
                  <span v-for="c in cdotList(row)" :key="c.key" class="cdot" :class="c.cls" :title="c.tip" />
                </span>
                <span v-else class="pod-pulse" :class="podPulseClass(row)" />
                <el-tag :type="statusColor(podPhase(row))" size="small">{{ podPhase(row) }}</el-tag>
                <span class="dim">{{ podReady(row) }}</span>
              </template>
              <template v-else-if="['deployments', 'statefulsets', 'daemonsets'].includes(cur.resource)">
                <el-tag v-if="isStopped(row)" type="danger" size="small">已停止</el-tag>
                <el-tag v-else size="small" type="info">副本 {{ deployReady(row) }}</el-tag>
                <span class="dim">{{ row.status?.updatedReplicas ?? row.status?.updatedNumberScheduled ?? 0 }} 已更新</span>
              </template>
              <template v-else-if="cur.resource === 'services'">
                <el-tag size="small" type="info">{{ row.spec?.type || 'ClusterIP' }}</el-tag>
                <span class="dim">{{ row.spec?.clusterIP }}</span>
              </template>
              <template v-else-if="cur.resource === 'namespaces'">
                <el-tag :type="statusColor(row.status?.phase)" size="small">{{ row.status?.phase }}</el-tag>
              </template>
              <template v-else-if="cur.resource === 'nodes'">
                <el-tag :type="nodeReady(row)" size="small">{{ nodeReady(row) === 'success' ? 'Ready' : 'NotReady' }}</el-tag>
                <el-tag v-if="row.spec?.unschedulable" type="warning" size="small">已封锁</el-tag>
                <el-tag v-for="r in nodeRolesOf(row)" :key="r" size="small" :type="r === 'worker' ? 'info' : 'warning'">{{ r }}</el-tag>
                <span class="dim">{{ row.status?.nodeInfo?.kubeletVersion }}</span>
              </template>
              <template v-else-if="cur.resource === 'configmaps' || cur.resource === 'secrets'">
                <el-tag size="small" type="info">{{ Object.keys(row.data || {}).length }} 项</el-tag>
              </template>
              <template v-else-if="cur.resource === 'persistentvolumeclaims'">
                <el-tag :type="statusColor(row.status?.phase)" size="small">{{ row.status?.phase }}</el-tag>
                <span class="dim">{{ row.spec?.resources?.requests?.storage }}</span>
              </template>
              <template v-else>
                <span class="dim">{{ row.kind }}</span>
              </template>
            </template>
          </el-table-column>
          <!-- Pod 专属：Pod IP + 节点 IP（带标签区分，点击复制；腾出空间来自命名空间/创建时间列的收窄） -->
          <el-table-column v-if="cur.resource === 'pods'" label="IP / 节点" width="175">
            <template #default="{ row }">
              <div class="pod-ip" title="点击复制" @click="copyText(row.status?.podIP)">Pod IP&nbsp;{{ row.status?.podIP || '—' }}</div>
              <div class="pod-node" title="点击复制" @click="copyText(row.status?.hostIP)">节点 IP&nbsp;{{ row.status?.hostIP || '—' }}</div>
            </template>
          </el-table-column>
          <!-- Node 专属：容量口径的 CPU/内存 + 扩展资源（GPU/NPU/DCU…，capacity 里除基础资源外的键） -->
          <el-table-column v-if="cur.resource === 'nodes'" label="CPU / 内存" width="145" sortable :sort-method="sortNodeCpu">
            <template #default="{ row }">
              <span>{{ fmtCores(nodeCpuCores(row)) }} / {{ fmtGi(nodeMemBytes(row)) }}</span>
            </template>
          </el-table-column>
          <el-table-column v-if="cur.resource === 'nodes'" label="设备资源" width="150">
            <template #default="{ row }">
              <template v-if="nodeExtRes(row).length">
                <el-tooltip v-for="x in nodeExtRes(row)" :key="x.name" :content="x.name" placement="top">
                  <el-tag size="small" type="warning" style="margin-right: 4px">{{ x.short }} {{ x.count }}</el-tag>
                </el-tooltip>
              </template>
              <span v-else class="dim">—</span>
            </template>
          </el-table-column>
          <!-- Ingress 专属：彩色行式路由（host 蓝 / path 绿 / 后端灰，同 host 留白分组；搜索支持按 host/path/后端服务命中，见后端 matchSearch） -->
          <el-table-column v-if="cur.resource === 'ingresses'" label="主机 / 路径" min-width="320">
            <template #default="{ row }">
              <template v-if="ingRoutes(row).length">
                <div v-for="r in ingRouteRows(row).slice(0, 3)" :key="r.key" class="ing-row">
                  <span class="ing-host">{{ r.showHost ? r.host : '' }}</span>
                  <span class="ing-path">{{ r.path }}</span>
                  <span class="ing-dim">→ {{ r.svc }}{{ r.port ? ':' + r.port : '' }}</span>
                </div>
                <el-tooltip v-if="ingRoutes(row).length > 3" :content="ingRouteDetail(row)" placement="top">
                  <el-tag size="small" type="info">+{{ ingRoutes(row).length - 3 }}</el-tag>
                </el-tooltip>
              </template>
              <span v-else class="dim">—</span>
            </template>
          </el-table-column>
          <!-- 创建时间列只显示 age 短文案（3d4h 级），90 足够；排序按真实时间戳 -->
          <el-table-column label="创建时间" prop="ctime" :width="cur.resource === 'pods' ? 90 : 105" sortable :sort-method="sortTime">
            <template #default="{ row }">{{ ageOf(row.metadata?.creationTimestamp) }}</template>
          </el-table-column>
          <!-- 操作列宽按资源区分：pods 按钮最多（日志/终端/下载/AI诊断/Describe/Grafana/退出/删除，紧凑 padding 后 470 单行放下）；
               deployments/statefulsets/daemonsets 多了 AI 诊断 + 启停/重启/伸缩/定时（460）；
               ingresses 只有 详情/删除/应用修改，收窄把宽度让给路由列 -->
          <el-table-column label="操作" :width="cur.resource === 'nodes' ? 330 : (cur.resource === 'ingresses' ? 170 : (cur.resource === 'pods' ? 470 : (['deployments', 'statefulsets', 'daemonsets'].includes(cur.resource) ? 460 : 400)))" fixed="right" class-name="ops-col">
            <template #default="{ row }">
              <el-button link type="primary" size="small" @click="openDetail(row)">详情</el-button>
              <el-tooltip v-if="rowDraftsOf(row).length" :content="`有 ${rowDraftsOf(row).length} 个模块的暂存修改待应用：${rowDraftsOf(row).map((d: any) => d.section_label || d.section).join('、')}`"
                placement="top">
                <el-button link type="success" size="small" :loading="applyingRow(row)" @click="applyRowDrafts(row)">应用修改</el-button>
              </el-tooltip>
              <template v-if="cur.resource === 'nodes'">
                <el-button v-if="verbAllowed(authResKey, 'update')" link :type="row.spec?.unschedulable ? 'success' : 'warning'" size="small"
                  :loading="nodeOpsLoading" @click="doNodeCordon(!row.spec?.unschedulable, row)">
                  {{ row.spec?.unschedulable ? '恢复调度' : '封锁调度' }}
                </el-button>
                <el-button v-if="verbAllowed(authResKey, 'update')" link type="danger" size="small" @click="openDrainDlg(row)">排空</el-button>
              </template>
              <template v-if="cur.resource === 'pods'">
                <el-button v-if="verbAllowed('pods', 'logs', row.metadata.namespace)" link type="success" size="small" @click="openLogs(row)">日志</el-button>
                <el-button v-if="verbAllowed('pods', 'exec', row.metadata.namespace)" link type="success" size="small" @click="openTerminal(row)"><span class="term-cursor">&gt;_</span> 终端</el-button>
                <el-button v-if="verbAllowed('pods', 'download', row.metadata.namespace) || verbAllowed('pods', 'upload', row.metadata.namespace)" link type="primary" size="small" @click="openFileBrowser(row)">{{ verbAllowed('pods', 'download', row.metadata.namespace) ? '下载' : '文件' }}</el-button>
                <el-button v-if="verbAllowed('pods', 'get', row.metadata.namespace)" link type="primary" size="small" @click="diagnosePod(row)">AI 诊断</el-button>
                <el-button v-if="verbAllowed('pods', 'get', row.metadata.namespace)" link type="primary" size="small" @click="openDescribe(row)">Describe</el-button>
                <el-tooltip v-if="grafanaBase" content="Grafana 监控" placement="top">
                  <button class="grafana-btn" @click="openGrafana(row)" type="button">
                    <img src="/img/grafana-icon.svg" alt="Grafana" />
                  </button>
                </el-tooltip>
                <el-button link type="warning" size="small" @click="openExitInfo(row)">退出详情</el-button>
                <el-button v-if="verbAllowed('pods', 'delete', row.metadata.namespace)" link type="danger" size="small" @click="doDelete(row)">删除</el-button>
              </template>
              <template v-if="['deployments', 'statefulsets'].includes(cur.resource)">
                <el-button v-if="verbAllowed(authResKey, 'get', row.metadata.namespace)" link type="primary" size="small" @click="diagnoseWorkload(row)">AI 诊断</el-button>
                <el-button v-if="isStopped(row) && actionAllowed(authResKey, 'start', row.metadata.namespace)" link type="success" size="small" @click="doStart(row)">启动</el-button>
                <el-button v-else-if="!isStopped(row) && actionAllowed(authResKey, 'stop', row.metadata.namespace)" link type="danger" size="small" @click="doStop(row)">停止</el-button>
                <el-button v-if="actionAllowed(authResKey, 'restart', row.metadata.namespace)" link type="warning" size="small" @click="doRestart(row)">重启</el-button>
                <el-button v-if="actionAllowed(authResKey, 'replicas', row.metadata.namespace)" link type="warning" size="small" @click="doScale(row)">伸缩</el-button>
                <el-button v-if="actionAllowed(authResKey, 'restart', row.metadata.namespace) || actionAllowed(authResKey, 'replicas', row.metadata.namespace)" link type="primary" size="small" @click="openSchedule(row)">定时</el-button>
                <el-button v-if="verbAllowed(authResKey, 'delete', row.metadata.namespace)" link type="danger" size="small" @click="doDelete(row)">删除</el-button>
              </template>
              <template v-if="cur.resource === 'daemonsets'">
                <!-- DaemonSet 副本由节点调度器管理：只支持滚动重启类操作 -->
                <el-button v-if="verbAllowed(authResKey, 'get', row.metadata.namespace)" link type="primary" size="small" @click="diagnoseWorkload(row)">AI 诊断</el-button>
                <el-button v-if="actionAllowed(authResKey, 'restart', row.metadata.namespace)" link type="warning" size="small" @click="doRestart(row)">重启</el-button>
                <el-button v-if="actionAllowed(authResKey, 'restart', row.metadata.namespace)" link type="primary" size="small" @click="openSchedule(row)">定时</el-button>
                <el-button v-if="verbAllowed(authResKey, 'delete', row.metadata.namespace)" link type="danger" size="small" @click="doDelete(row)">删除</el-button>
              </template>
              <!-- Job/CronJob/ReplicaSet 走通用尾巴（只有删除）：补 AI 诊断入口，后端同口径支持 -->
              <el-button v-if="['jobs', 'cronjobs', 'replicasets'].includes(cur.resource) && verbAllowed(authResKey, 'get', row.metadata.namespace)" link type="primary" size="small" @click="diagnoseWorkload(row)">AI 诊断</el-button>
              <!-- nodes 删除收进详情页（双重确认）；其余资源保留通用尾巴 -->
              <el-button v-if="!['pods', 'deployments', 'statefulsets', 'daemonsets', 'nodes'].includes(cur.resource) && verbAllowed(authResKey, 'delete', row.metadata.namespace)" link type="danger" size="small" @click="doDelete(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-card>
    </div>

    <!-- 详情抽屉：运行实例/节点概要 → 配置 → YAML -->
    <el-drawer v-model="detailDlg" size="72%" :title="detailTitle" destroy-on-close>
      <div class="drawer-body">
        <el-tabs v-model="detailTab" class="detail-tabs" @tab-change="onDetailTab">
          <!-- Node 专属概要：角色 + CPU/内存/异构资源（容量/可分配/已分配 + Pod 分配明细） -->
          <el-tab-pane v-if="cur.resource === 'nodes'" label="概要" name="overview" lazy>
            <div v-loading="overviewLoading">
              <el-alert v-if="overviewErr" type="warning" :title="overviewErr" :closable="false" />
              <template v-else-if="overview">
                <!-- 节点运维栏：封锁/恢复调度（轻量可逆）、排空（长耗时，对话框勾选）、删除（收进详情双确认） -->
                <el-alert v-if="detailTarget?.spec?.unschedulable" type="warning" :closable="false" style="margin-bottom: 8px"
                  title="该节点已封锁调度（unschedulable）：新 Pod 不会调度上来，存量 Pod 不受影响" />
                <div style="margin-bottom: 12px; display: flex; align-items: center; gap: 8px">
                  <el-button v-if="!detailTarget?.spec?.unschedulable && verbAllowed(authResKey, 'update')" size="small" type="warning" plain :loading="nodeOpsLoading"
                    @click="doNodeCordon(true)">封锁调度</el-button>
                  <el-button v-else-if="detailTarget?.spec?.unschedulable && verbAllowed(authResKey, 'update')" size="small" type="success" plain :loading="nodeOpsLoading"
                    @click="doNodeCordon(false)">恢复调度</el-button>
                  <el-button v-if="verbAllowed(authResKey, 'update')" size="small" type="danger" @click="openDrainDlg">排空</el-button>
                  <el-button v-if="verbAllowed(authResKey, 'delete')" size="small" type="danger" plain @click="doDeleteNode">删除节点</el-button>
                  <span class="dim" style="font-size: 12px">删除已收进详情页并双重确认，避免列表误触</span>
                </div>
                <el-descriptions :column="3" size="small" border style="margin-bottom: 12px">
                  <el-descriptions-item label="角色">
                    <el-tag v-for="r in overview.roles" :key="r" size="small" :type="r === 'worker' ? 'info' : 'warning'" style="margin-right: 4px">{{ r }}</el-tag>
                  </el-descriptions-item>
                  <el-descriptions-item label="内网 IP">{{ overview.internal_ip || '-' }}</el-descriptions-item>
                  <el-descriptions-item label="节点名" :span="2">{{ detailTarget?.metadata?.name || '-' }}</el-descriptions-item>
                  <el-descriptions-item label="创建时间">{{ fmtTs(detailTarget?.metadata?.creationTimestamp) }}</el-descriptions-item>
                  <el-descriptions-item label="kubelet">{{ overview.kubelet_version || '-' }}</el-descriptions-item>
                  <el-descriptions-item label="操作系统" :span="2">{{ overview.os_image || '-' }}</el-descriptions-item>
                  <el-descriptions-item label="内核">{{ overview.kernel_version || '-' }}</el-descriptions-item>
                  <el-descriptions-item label="容器运行时">{{ overview.container_runtime_version || '-' }}</el-descriptions-item>
                  <el-descriptions-item label="架构">{{ overview.architecture || '-' }}</el-descriptions-item>
                </el-descriptions>

                <div style="margin-bottom: 12px">
                  <span class="dim" style="margin-right: 8px">状态</span>
                  <el-tooltip v-for="cd in overview.conditions || []" :key="cd.type" :disabled="cd.status === 'True'"
                    :content="`${cd.reason || ''} ${cd.message || ''}`.trim()" placement="top">
                    <el-tag size="small" :type="cd.status === 'True' ? 'success' : 'danger'" style="margin-right: 4px">{{ cd.type }}</el-tag>
                  </el-tooltip>
                </div>
                <div v-if="overview.taints?.length" style="margin-bottom: 12px">
                  <span class="dim" style="margin-right: 8px">污点</span>
                  <el-tag v-for="t in overview.taints" :key="t.key + t.effect" size="small" type="warning" style="margin-right: 4px">
                    {{ t.value ? `${t.key}=${t.value}` : t.key }}:{{ t.effect }}
                  </el-tag>
                </div>

                <el-table :data="overview.resources" size="small" border style="margin-bottom: 12px">
                  <el-table-column label="资源" min-width="170">
                    <template #default="{ row }">
                      <el-tooltip v-if="row.kind === 'extended'" :content="row.name" placement="top">
                        <span>{{ extResShort(row.name) }}</span>
                      </el-tooltip>
                      <span v-else>{{ resKindLabel(row.kind) }}</span>
                    </template>
                  </el-table-column>
                  <el-table-column label="容量" width="110">
                    <template #default="{ row }">{{ fmtResQty(row.unit, row.capacity) }}</template>
                  </el-table-column>
                  <el-table-column label="可分配" width="110">
                    <template #default="{ row }">{{ fmtResQty(row.unit, row.allocatable) }}</template>
                  </el-table-column>
                  <el-table-column label="已分配" width="110">
                    <template #default="{ row }">{{ fmtAllocated(row.unit, row.allocated) }}</template>
                  </el-table-column>
                  <el-table-column label="分配率" min-width="150">
                    <template #default="{ row }">
                      <el-progress :percentage="allocPctOf(row)" :color="pctColor" :stroke-width="12" />
                    </template>
                  </el-table-column>
                </el-table>

                <div class="pods-toolbar">
                  <span class="dim">该节点上的 Pod（按 CPU requests 降序，最多 500 条）</span>
                  <el-tooltip v-if="grafanaBase" content="Grafana 监控" placement="top">
                    <button class="grafana-btn" @click="openGrafanaNode" type="button" style="margin-left: 8px">
                      <img src="/img/grafana-icon.svg" alt="Grafana" />
                    </button>
                  </el-tooltip>
                  <el-button size="small" :loading="overviewLoading" style="margin-left: 8px" @click="loadOverview">刷新</el-button>
                </div>
                <el-alert v-if="overview.pods_filtered" type="info" :closable="false" style="margin-bottom: 8px"
                  title="明细仅显示你有 pods list 权限的命名空间；上方汇总数字为该节点全量口径" />
                <el-table :data="overview.pods" size="small" border empty-text="该节点暂无可见 Pod">
                  <el-table-column prop="namespace" label="命名空间" min-width="130" show-overflow-tooltip />
                  <el-table-column prop="name" label="Pod" min-width="240" show-overflow-tooltip />
                  <el-table-column label="状态" width="100">
                    <template #default="{ row }"><el-tag :type="statusColor(row.phase)" size="small">{{ row.phase }}</el-tag></template>
                  </el-table-column>
                  <el-table-column label="CPU" width="90">
                    <template #default="{ row }">{{ fmtMilli(row.cpu_milli) }}</template>
                  </el-table-column>
                  <el-table-column label="内存" width="100">
                    <template #default="{ row }">{{ fmtMemBytes(row.mem_bytes) }}</template>
                  </el-table-column>
                  <el-table-column label="设备" min-width="140">
                    <template #default="{ row }">
                      <template v-if="row.ext && Object.keys(row.ext).length">
                        <el-tooltip v-for="(v, k) in row.ext" :key="k" :content="String(k)" placement="top">
                          <el-tag size="small" type="warning" style="margin-right: 4px">{{ extResShort(String(k)) }} {{ v }}</el-tag>
                        </el-tooltip>
                      </template>
                      <span v-else class="dim">—</span>
                    </template>
                  </el-table-column>
                </el-table>
                <div class="dim" style="font-size: 12px; margin-top: 8px">
                  分配率 = 该节点 Pod requests 之和 / 可分配，与集群总览同口径；实时利用率请查看 Grafana。
                </div>
              </template>
            </div>
          </el-tab-pane>
          <el-tab-pane v-if="hasPodsView" label="运行实例" name="pods" lazy>
            <div class="pods-toolbar">
              <span class="dim">该应用当前的运行实例（Pod）</span>
              <el-button size="small" :loading="appPodsLoading" style="margin-left: auto" @click="loadAppPods">刷新</el-button>
            </div>
            <el-alert v-if="appPodsErr" type="warning" :title="appPodsErr" :closable="false" />
            <el-table v-else :data="appPods" size="small" border v-loading="appPodsLoading" empty-text="暂无运行实例">
              <el-table-column prop="metadata.name" label="实例名" min-width="240" show-overflow-tooltip />
              <el-table-column label="状态" width="100">
                <template #default="{ row }"><el-tag :type="statusColor(podPhase(row))" size="small">{{ podPhase(row) }}</el-tag></template>
              </el-table-column>
              <el-table-column label="重启次数" width="90">
                <template #default="{ row }">{{ podRestarts(row) }}</template>
              </el-table-column>
              <el-table-column label="所在节点" min-width="130" show-overflow-tooltip>
                <template #default="{ row }"><span class="dim">{{ row.spec?.nodeName || '-' }}</span></template>
              </el-table-column>
              <el-table-column label="运行时长" width="110">
                <template #default="{ row }">{{ ageOf(row.metadata?.creationTimestamp) }}</template>
              </el-table-column>
              <el-table-column label="操作" width="350">
                <template #default="{ row }">
                  <el-button v-if="verbAllowed('pods', 'logs', row.metadata.namespace)" link type="success" size="small" @click="openLogs(row)">日志</el-button>
                  <el-button v-if="verbAllowed('pods', 'exec', row.metadata.namespace)" link type="success" size="small" @click="openTerminal(row)"><span class="term-cursor">&gt;_</span> 终端</el-button>
                  <el-button v-if="verbAllowed('pods', 'get', row.metadata.namespace)" link type="primary" size="small" @click="diagnosePod(row)">AI 诊断</el-button>
                  <el-tooltip v-if="grafanaBase" content="Grafana 监控" placement="top">
                    <button class="grafana-btn" @click="openGrafana(row)" type="button">
                      <img src="/img/grafana-icon.svg" alt="Grafana" />
                    </button>
                  </el-tooltip>
                  <el-button v-if="verbAllowed('pods', 'delete', row.metadata.namespace)" link type="danger" size="small" @click="doDeletePod(row)">删除</el-button>
                  <el-button link type="warning" size="small" @click="openExitInfo(row)">退出详情</el-button>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>
          <el-tab-pane v-if="formSections.length" :label="cur.resource === 'nodes' ? '节点配置' : '应用编辑'" name="form">
            <el-alert type="info" :closable="false" style="margin-bottom: 10px"
              title="按模块单独保存；各模块对应一项授权动作（无权限的模块会被拒绝），标签与注解仅需 update 权限。" />
            <el-tabs v-model="formTab" tab-position="left" class="form-tabs form-edit">
              <el-tab-pane v-for="s in formSections" :key="s.key" :label="sectionEditable(s.key) ? (draftFor(s.key) ? s.label + ' ●' : s.label) : s.label + '（只读）'" :name="s.key">
                <el-alert v-if="!sectionEditable(s.key)" type="info" :closable="false" style="margin-bottom: 10px"
                  title="无该模块的编辑权限（仅查看）" />
                <!-- 暂存 banner：当前模块有暂存的修改 -->
                <el-alert v-if="draftOfCurrent" type="warning" :closable="false" style="margin-bottom: 10px">
                  <template #title>
                    <div class="draft-banner">
                      <span>该模块有暂存的修改（{{ fmtDraftTime(draftOfCurrent.updated_at) }}，{{ draftOfCurrent.created_by }}）——未写入集群</span>
                      <span class="draft-actions">
                        <el-button size="small" type="primary" link @click="loadDraftToForm(draftOfCurrent)">载入表单</el-button>
                        <el-button size="small" type="success" link :loading="applying" @click="applyDraft(draftOfCurrent, { reloadAfter: true })">直接应用</el-button>
                        <el-button size="small" type="danger" link @click="discardDraft(draftOfCurrent, { reloadAfter: true })">放弃</el-button>
                      </span>
                    </div>
                  </template>
                </el-alert>
                <div :class="{ 'form-readonly': !sectionEditable(s.key) }">
                <!-- 标签 / 注解（metadata 层，所有可编辑资源通用） -->
                <template v-if="s.key === 'labels' || s.key === 'annotations'">
                  <el-alert type="info" :closable="false" style="margin-bottom: 10px"
                    :title="s.key === 'labels' ? '标签（labels）：用于选择器匹配与分组查询，值限 63 字符内字母/数字/-/_' : '注解（annotations）：附加说明信息（如负责人、变更记录），值可为任意文本'"
                    description="保存为全量覆盖：删除一行即删除该条目；系统内置标签（如 app、控制器的 selector 相关标签）被删可能导致 Pod 失配，请谨慎操作。" />
                  <el-table :data="metaForm[s.key]" border>
                    <el-table-column label="键 (key)" min-width="220">
                      <template #default="{ row }">
                        <el-input v-model="row.key" placeholder="如 app / team / example.com/owner" />
                      </template>
                    </el-table-column>
                    <el-table-column label="值 (value)" min-width="220">
                      <template #default="{ row }">
                        <el-input v-model="row.value" :type="s.key === 'annotations' ? 'textarea' : 'text'" :autosize="{ minRows: 1, maxRows: 4 }"
                          :placeholder="s.key === 'labels' ? '如 web（≤63 字符，字母数字-_.）' : '任意文本，如 负责人/描述'" />
                      </template>
                    </el-table-column>
                    <el-table-column label="操作" width="80">
                      <template #default="{ $index }">
                        <el-button link type="danger" @click="metaForm[s.key].splice($index, 1)">删除</el-button>
                      </template>
                    </el-table-column>
                  </el-table>
                  <div style="margin-top: 10px; display: flex; align-items: center; gap: 10px">
                    <el-button @click="metaForm[s.key].push({ key: '', value: '' })">+ 添加{{ s.key === 'labels' ? '标签' : '注解' }}</el-button>
                    <span class="dim" style="font-size: 12px">共 {{ metaForm[s.key].length }} 条</span>
                  </div>
                </template>
                <!-- ConfigMap / Secret：数据（key-value 全量覆盖） -->
                <template v-else-if="cur.resource === 'configmaps' || cur.resource === 'secrets'">
                  <el-alert type="info" :closable="false" style="margin-bottom: 10px"
                    :title="cur.resource === 'configmaps' ? '配置数据（data）：键值对，值为文本（YAML / JSON / properties 均可）' : '机密数据（data）：值为明文，保存时自动 base64 编码；标有二进制的条目只读'"
                    description="保存为全量覆盖：删除一行即删除该键；正在引用该配置的 Pod 不会自动生效，需重启应用。" />
                  <el-table :data="dataForm.rows" border>
                    <el-table-column label="键 (key)" width="240">
                      <template #default="{ row }">
                        <el-input v-model="row.key" placeholder="如 config.yaml / DB_PASSWORD" />
                      </template>
                    </el-table-column>
                    <el-table-column label="值 (value)" min-width="420">
                      <template #default="{ row }">
                        <el-input v-if="!row.binary" v-model="row.value" type="textarea" :autosize="{ minRows: 2, maxRows: 12 }" placeholder="配置内容" />
                        <el-input v-else model-value="（二进制内容，请使用 YAML 编辑）" disabled />
                      </template>
                    </el-table-column>
                    <el-table-column label="操作" width="80">
                      <template #default="{ $index }">
                        <el-button link type="danger" @click="dataForm.rows.splice($index, 1)">删除</el-button>
                      </template>
                    </el-table-column>
                  </el-table>
                  <div style="margin-top: 10px; display: flex; align-items: center; gap: 10px">
                    <el-button @click="dataForm.rows.push({ key: '', value: '' })">+ 添加数据项</el-button>
                    <span class="dim" style="font-size: 12px">共 {{ dataForm.rows.length }} 项</span>
                  </div>
                </template>
                <!-- PVC：容量扩容 -->
                <template v-else-if="cur.resource === 'persistentvolumeclaims'">
                  <el-alert type="info" :closable="false" style="margin-bottom: 10px"
                    title="扩容容量：仅当存储类开启扩容（allowVolumeExpansion）时生效，且只能扩大不能缩小；存储类与访问模式创建后不可修改。" />
                  <el-form label-width="100px" style="max-width: 560px">
                    <el-form-item label="当前容量"><span class="mono">{{ pvcCapForm.cur || '-' }}</span></el-form-item>
                    <el-form-item label="存储类"><span class="mono">{{ pvcCapForm.storageClass || '（集群默认）' }}</span></el-form-item>
                    <el-form-item label="访问模式"><span class="mono">{{ pvcCapForm.accessMode || '-' }}</span></el-form-item>
                    <el-form-item label="新容量" required>
                      <el-input v-model="pvcCapForm.size" placeholder="如 20Gi（需大于当前容量）" style="width: 220px" />
                    </el-form-item>
                  </el-form>
                </template>
                <!-- Ingress：路由规则（spec.rules 行式编辑） -->
                <template v-else-if="cur.resource === 'ingresses'">
                  <el-alert type="info" :closable="false" style="margin-bottom: 10px"
                    title="路由规则：域名 + 路径 + 后端服务；同域名相邻的行保存时自动合并为一条规则（rule）"
                    description="保存为 spec.rules 全量覆盖；TLS、默认后端与 Ingress 类不在此表单中，请使用 YAML 编辑。" />
                  <el-table :data="ingForm.rows" border>
                    <el-table-column label="域名" min-width="200">
                      <template #default="{ row }"><el-input v-model="row.host" placeholder="app.example.com（留空通配）" /></template>
                    </el-table-column>
                    <el-table-column label="路径" width="140">
                      <template #default="{ row }"><el-input v-model="row.path" placeholder="/" /></template>
                    </el-table-column>
                    <el-table-column label="路径类型" width="180">
                      <template #default="{ row }">
                        <el-select v-model="row.pathType">
                          <el-option value="Prefix" label="Prefix（前缀匹配）" />
                          <el-option value="Exact" label="Exact（精确匹配）" />
                          <el-option value="ImplementationSpecific" label="ImplementationSpecific" />
                        </el-select>
                      </template>
                    </el-table-column>
                    <el-table-column label="后端服务" min-width="190">
                      <template #default="{ row }">
                        <el-select v-model="row.svc" filterable allow-create default-first-option placeholder="选择或输入服务名">
                          <el-option v-for="n in svcNameOptions" :key="n" :label="n" :value="n" />
                        </el-select>
                      </template>
                    </el-table-column>
                    <el-table-column label="服务端口" width="140">
                      <template #default="{ row }"><el-input v-model="row.port" placeholder="端口或名称" /></template>
                    </el-table-column>
                    <el-table-column label="操作" width="80">
                      <template #default="{ $index }"><el-button link type="danger" @click="ingForm.rows.splice($index, 1)">删除</el-button></template>
                    </el-table-column>
                  </el-table>
                  <div style="margin-top: 10px; display: flex; align-items: center; gap: 10px">
                    <el-button @click="ingForm.rows.push({ host: '', path: '/', pathType: 'Prefix', svc: '', port: '80' })">+ 添加路由</el-button>
                    <span class="dim" style="font-size: 12px">共 {{ ingForm.rows.length }} 条</span>
                  </div>
                </template>
                <!-- Service：端口 / 选择器 -->
                <template v-else-if="cur.resource === 'services'">
                  <template v-if="formTab === 'ports'">
                    <el-table :data="svcForm.ports" border class="svc-ports-table">
                      <el-table-column label="名称" min-width="160"><template #default="{ row }"><el-input v-model="row.name" placeholder="可选" /></template></el-table-column>
                      <el-table-column label="端口" width="190"><template #default="{ row }"><el-input-number v-model="row.port" :min="1" :max="65535" controls-position="right" style="width: 160px" /></template></el-table-column>
                      <el-table-column label="目标端口" width="180"><template #default="{ row }"><el-input v-model="row.targetPort" placeholder="端口或名称" /></template></el-table-column>
                      <el-table-column label="协议" width="130"><template #default="{ row }"><el-select v-model="row.protocol"><el-option value="TCP" /><el-option value="UDP" /></el-select></template></el-table-column>
                      <el-table-column label="NodePort" width="170"><template #default="{ row }"><el-input-number v-model="row.nodePort" :min="0" :max="65535" controls-position="right" style="width: 150px" placeholder="留空自动" /></template></el-table-column>
                      <el-table-column label="操作" width="80"><template #default="{ $index }"><el-button link type="danger" @click="svcForm.ports.splice($index, 1)">删除</el-button></template></el-table-column>
                    </el-table>
                    <el-button style="margin-top: 10px" @click="svcForm.ports.push({ name: '', port: null, targetPort: '', protocol: 'TCP', nodePort: null })">+ 添加端口</el-button>
                  </template>
                  <template v-else>
                    <el-table :data="svcForm.selector" border>
                      <el-table-column label="Key" min-width="180"><template #default="{ row }"><el-input v-model="row.key" placeholder="如 app" /></template></el-table-column>
                      <el-table-column label="Value" min-width="220"><template #default="{ row }"><el-input v-model="row.value" placeholder="如 web" /></template></el-table-column>
                      <el-table-column label="操作" width="80"><template #default="{ $index }"><el-button link type="danger" @click="svcForm.selector.splice($index, 1)">删除</el-button></template></el-table-column>
                    </el-table>
                    <el-button style="margin-top: 10px" @click="svcForm.selector.push({ key: '', value: '' })">+ 添加标签</el-button>
                  </template>
                </template>
                <!-- 节点：污点编辑（spec.taints 全量覆盖；权限=nodes 的 update，与封锁调度同口径） -->
                <template v-else-if="s.key === 'taints'">
                  <el-alert type="info" :closable="false" style="margin-bottom: 10px"
                    title="污点（taints）：让节点排斥不容忍该污点的 Pod；配合 Pod 的 tolerations 可实现专用节点隔离"
                    description="保存为 spec.taints 全量覆盖：删除一行即删除该污点。effect 含义——NoSchedule 不调度新 Pod；PreferNoSchedule 尽量不调度；NoExecute 驱逐已运行的不容忍 Pod。" />
                  <el-table :data="taintForm.rows" border>
                    <el-table-column label="污点（键 = 值）" min-width="380">
                      <template #default="{ row }">
                        <div class="taint-key-cell">
                          <el-select :model-value="taintKvOf(row)" :disabled="isSystemTaint(row.key)" filterable allow-create default-first-option clearable
                            placeholder="下拉选择节点现有「键 = 值」污点或输入新键" style="width: 100%"
                            @update:model-value="(v: string) => setTaintKv(row, v)">
                            <el-option v-for="p in taintPairs" :key="p" :label="p" :value="p" />
                          </el-select>
                          <el-tag v-if="isSystemTaint(row.key)" size="small" type="info" effect="plain">系统</el-tag>
                        </div>
                      </template>
                    </el-table-column>
                    <el-table-column label="效果 (effect)" width="190">
                      <template #default="{ row }">
                        <el-select v-model="row.effect" :disabled="isSystemTaint(row.key)">
                          <el-option value="NoSchedule" label="NoSchedule（不调度）" />
                          <el-option value="PreferNoSchedule" label="PreferNoSchedule（尽量不调度）" />
                          <el-option value="NoExecute" label="NoExecute（不调度并驱逐存量）" />
                        </el-select>
                      </template>
                    </el-table-column>
                    <el-table-column label="操作" width="80">
                      <template #default="{ $index }">
                        <!-- 系统污点行只读：kubelet/控制器维护，删了也会被改回（后端同样拒绝删改） -->
                        <el-button link type="danger" :disabled="isSystemTaint(taintForm.rows[$index].key)" @click="taintForm.rows.splice($index, 1)">删除</el-button>
                      </template>
                    </el-table-column>
                  </el-table>
                  <el-alert v-if="taintSystemRows.length" type="warning" :closable="false" style="margin-top: 10px"
                    :title="`系统管理型污点（${taintSystemRows.join('、')}）由 kubelet/控制器维护，只能原样保留（行已只读，删除/修改会被拒绝）`" />
                  <div style="margin-top: 10px; display: flex; align-items: center; gap: 10px">
                    <el-button @click="taintForm.rows.push({ key: '', value: '', effect: 'NoSchedule' })">+ 添加污点</el-button>
                    <span class="dim" style="font-size: 12px">共 {{ taintForm.rows.length }} 条</span>
                  </div>
                </template>
                <!-- 工作负载：镜像 / 端口 / 资源 / 环境变量 -->
                <template v-else>
                  <!-- 调度策略：Pod 级配置（非容器级），渲染在容器循环外 -->
                  <template v-if="formTab === 'scheduling'">
                    <el-alert v-if="schedUnmodeledFlag" type="warning" :closable="false" style="margin-bottom: 10px"
                      title="该工作负载存在表单不支持的高级亲和配置（多 term / matchFields / Pod 亲和等），保存将被拒绝；如需保留请用 YAML 编辑" />
                    <el-alert type="info" :closable="false" style="margin-bottom: 10px"
                      title="调度策略：控制 Pod 落在哪些节点。修改后更新 Pod 模板，保存触发滚动重启（Pod 逐个重建）。"
                      description="硬性约束不满足时 Pod 一直 Pending；偏好仅影响打分排序。已有 Pod 不自动迁移。" />
                    <SchedEditor v-model="schedForm" :nodes="nodeOptions" :label-keys="nodeLabelKeys" :label-values="nodeLabelValues" />
                  </template>
                  <!-- 镜像历史（Deployment/StatefulSet/DaemonSet：K8s 原生版本记录，选历史一键回填） -->
                  <!-- v-if 而非 v-else-if：bar 与下方容器循环并列展示（此前误接成 else-if 链导致镜像框被排挤掉） -->
                  <div v-if="formTab === 'image' && imgHistSupported" class="img-hist-bar">
                    <el-button size="small" @click="openImageHistory">历史版本</el-button>
                    <span class="dim">来自 K8s 原生版本记录（默认保留最近 10 代），选历史版本回填镜像后仍需保存（走镜像编辑权限）</span>
                  </div>
                  <div v-for="ct in formModel" :key="ct.name" class="ct-block">
                    <div class="ct-name">容器：{{ ct.name }}</div>
                    <template v-if="formTab === 'image'">
                      <el-input v-model="ct.image" size="large" placeholder="镜像地址，如 registry.example.com/app:v1.2">
                        <template #prepend>镜像</template>
                      </el-input>
                    </template>
                    <template v-else-if="formTab === 'command'">
                      <div class="cmd-block">
                        <div class="cmd-row">
                          <span class="cmd-label">入口 (Entrypoint)</span>
                          <el-input v-model="ct.command" placeholder="例如: /bin/sh；留空 = 保持镜像默认 ENTRYPOINT" />
                        </div>
                        <div class="cmd-row">
                          <span class="cmd-label">命令</span>
                          <el-input v-model="ct.args" type="textarea" :autosize="{ minRows: 2, maxRows: 8 }"
                            placeholder="入口为 shell（如 /bin/sh）时直接写要执行的命令，多行 = 多条依序执行（无需 -c）；入口为其他程序或留空时命令每行一个参数（如&#10;-f&#10;httpd.conf）" />
                        </div>
                        <div class="cmd-tip dim">清除全部内容 = 恢复镜像默认 ENTRYPOINT / CMD；修改后新 Pod 生效（存量 Pod 不会自动重启）。</div>
                      </div>
                    </template>
                    <template v-else-if="formTab === 'ports'">
                      <el-table :data="ct.ports" border class="ports-table">
                        <el-table-column label="名称" min-width="180"><template #default="{ row }"><el-input v-model="row.name" placeholder="可选" /></template></el-table-column>
                        <el-table-column label="容器端口" width="190"><template #default="{ row }"><el-input-number v-model="row.containerPort" :min="1" :max="65535" controls-position="right" style="width: 160px" /></template></el-table-column>
                        <el-table-column label="主机端口" width="190"><template #default="{ row }"><el-input-number v-model="row.hostPort" :min="1" :max="65535" controls-position="right" placeholder="可选" style="width: 160px" /></template></el-table-column>
                        <el-table-column label="协议" width="130"><template #default="{ row }"><el-select v-model="row.protocol"><el-option value="TCP" /><el-option value="UDP" /></el-select></template></el-table-column>
                        <el-table-column label="操作" width="80"><template #default="{ $index }"><el-button link type="danger" @click="ct.ports.splice($index, 1)">删除</el-button></template></el-table-column>
                      </el-table>
                      <div class="dim" style="font-size: 12px; margin: 8px 0 10px">主机端口（hostPort）会把容器端口直接映射到宿主机，仅在确有需要（如 DaemonSet 节点级暴露）时填写；留空不映射。</div>
                      <el-button style="margin-top: 0" @click="ct.ports.push({ name: '', containerPort: null, hostPort: null, protocol: 'TCP' })">+ 添加端口</el-button>
                    </template>
                    <template v-else-if="formTab === 'resources'">
                      <div class="res-grid">
                        <div class="res-item"><span>CPU 限制</span>
                          <div class="qty-row">
                            <el-input v-model="ct.resources.limits.cpu.num" placeholder="如 1，留空不限" />
                            <el-select v-model="ct.resources.limits.cpu.unit" class="qty-unit">
                              <el-option value="m" label="m 毫核" /><el-option value="core" label="c（核）" />
                            </el-select>
                          </div>
                        </div>
                        <div class="res-item"><span>内存限制</span>
                          <div class="qty-row">
                            <el-input v-model="ct.resources.limits.memory.num" placeholder="如 2" />
                            <el-select v-model="ct.resources.limits.memory.unit" class="qty-unit">
                              <el-option value="Mi" label="Mi" /><el-option value="Gi" label="Gi" />
                              <el-option value="Ki" label="Ki" /><el-option value="Ti" label="Ti" />
                            </el-select>
                          </div>
                        </div>
                        <div class="res-item"><span>CPU 请求</span>
                          <div class="qty-row">
                            <el-input v-model="ct.resources.requests.cpu.num" placeholder="如 0.5，留空不限" />
                            <el-select v-model="ct.resources.requests.cpu.unit" class="qty-unit">
                              <el-option value="m" label="m 毫核" /><el-option value="core" label="c（核）" />
                            </el-select>
                          </div>
                        </div>
                        <div class="res-item"><span>内存请求</span>
                          <div class="qty-row">
                            <el-input v-model="ct.resources.requests.memory.num" placeholder="如 1" />
                            <el-select v-model="ct.resources.requests.memory.unit" class="qty-unit">
                              <el-option value="Mi" label="Mi" /><el-option value="Gi" label="Gi" />
                              <el-option value="Ki" label="Ki" /><el-option value="Ti" label="Ti" />
                            </el-select>
                          </div>
                        </div>
                      </div>
                      <div class="dim" style="font-size: 12px; margin: 12px 0 6px">异构资源（GPU / NPU 等，计入上限；保存时原样保留）：</div>
                      <el-table :data="ct.resources.extended" size="small" border class="fd-table">
                        <el-table-column label="资源名" min-width="240">
                          <template #default="{ row }">
                            <el-input v-model="row.key" placeholder="nvidia.com/gpu / huawei.com/Ascend910" />
                          </template>
                        </el-table-column>
                        <el-table-column label="数量" min-width="120">
                          <template #default="{ row }"><el-input v-model="row.value" placeholder="1" /></template>
                        </el-table-column>
                        <el-table-column label="操作" width="80">
                          <template #default="{ $index }">
                            <el-button link type="danger" @click="ct.resources.extended.splice($index, 1)">删除</el-button>
                          </template>
                        </el-table-column>
                      </el-table>
                      <el-button style="margin-top: 8px" @click="ct.resources.extended.push({ key: '', value: '' })">+ 添加异构资源</el-button>
                      <div v-if="formResourceBoundsError(ct)" class="resource-validation-error" role="alert">
                        {{ formResourceBoundsError(ct) }}
                      </div>
                    </template>
                    <template v-else-if="formTab === 'env'">
                      <el-table :data="ct.env" border>
                        <el-table-column label="变量名" min-width="250"><template #default="{ row }"><el-input v-model="row.name" placeholder="如 ENV_NAME" /></template></el-table-column>
                        <el-table-column label="类型" width="140"><template #default="{ row }">
                          <el-select v-model="row.type">
                            <el-option value="value" label="自定义值" />
                            <el-option value="secret" label="Secret 引用" />
                            <el-option value="configmap" label="ConfigMap 引用" />
                            <el-option value="fieldref" label="字段引用" />
                          </el-select>
                        </template></el-table-column>
                        <el-table-column label="值" min-width="270"><template #default="{ row }"><el-input v-model="row.value" :disabled="row.type !== 'value'" placeholder="变量值" /></template></el-table-column>
                        <el-table-column label="引用对象名" min-width="180"><template #default="{ row }">
                          <el-select v-if="row.type === 'secret' || row.type === 'configmap'" v-model="row.refName"
                            filterable allow-create default-first-option placeholder="选择或输入对象名"
                            style="width: 100%" @focus="loadRefOptions(row.type, editNS())"
                            @change="row.refKey = ''; loadEnvKeys(row.type, editNS(), row.refName)">
                            <el-option v-for="n in refOptsOf(row.type, editNS())" :key="n" :label="n" :value="n" />
                          </el-select>
                          <el-input v-else v-model="row.refName" disabled placeholder="Secret/CM 名" />
                        </template></el-table-column>
                        <el-table-column label="引用键" min-width="150"><template #default="{ row }">
                          <el-select v-if="row.type === 'secret' || row.type === 'configmap'" v-model="row.refKey"
                            filterable allow-create default-first-option
                            :placeholder="row.refName ? '选择或输入 key' : '先选引用对象名'" style="width: 100%"
                            @focus="loadEnvKeys(row.type, editNS(), row.refName)">
                            <el-option v-for="k in envKeyOpts(row.type, editNS(), row.refName)" :key="k" :label="k" :value="k" />
                          </el-select>
                          <el-input v-else v-model="row.refKey" :disabled="row.type === 'value'"
                            :placeholder="row.type === 'fieldref' ? '字段路径，如 metadata.name' : 'key'" />
                        </template></el-table-column>
                        <el-table-column label="操作" width="80"><template #default="{ $index }"><el-button link type="danger" @click="ct.env.splice($index, 1)">删除</el-button></template></el-table-column>
                      </el-table>
                      <el-button style="margin-top: 10px" @click="ct.env.push({ name: '', type: 'value', value: '', refName: '', refKey: '' })">+ 添加变量</el-button>
                    </template>
                    <template v-else-if="formTab === 'probes'">
                      <div v-for="pk in probeKeys" :key="pk" class="probe-card">
                        <!-- 标题行：探针名 + 类型下拉（默认尺寸） + raw 兜底提示 -->
                        <div class="probe-head">
                          <span class="probe-title">{{ probeMeta[pk].label }}</span>
                          <el-select v-model="ct.probes[pk].type" style="width: 150px" @change="ct.probes[pk].raw = null">
                            <el-option value="none" label="不启用" />
                            <el-option value="http" label="HTTP 请求" />
                            <el-option value="tcp" label="TCP 端口" />
                            <el-option value="exec" label="执行命令" />
                          </el-select>
                          <span v-if="ct.probes[pk].raw" class="dim" style="font-size: 12px">
                            已有平台不支持编辑的探针类型，未改动时保存原样保留
                          </span>
                        </div>
                        <!-- 探测方式行：http = 路径+协议+端口；tcp = 端口；exec = 命令 -->
                        <div v-if="ct.probes[pk].type !== 'none'" class="probe-target">
                          <template v-if="ct.probes[pk].type === 'http'">
                            <div class="pt-item" style="flex: 1">
                              <span class="pt-label">路径</span>
                              <el-input v-model="ct.probes[pk].path" placeholder="如 /healthz" />
                            </div>
                            <div class="pt-item">
                              <span class="pt-label">协议</span>
                              <el-select v-model="ct.probes[pk].scheme" style="width: 100px">
                                <el-option value="HTTP" label="HTTP" />
                                <el-option value="HTTPS" label="HTTPS" />
                              </el-select>
                            </div>
                            <div class="pt-item">
                              <span class="pt-label">端口</span>
                              <el-input v-model="ct.probes[pk].port" placeholder="数字或端口名" style="width: 150px" />
                            </div>
                          </template>
                          <template v-else-if="ct.probes[pk].type === 'tcp'">
                            <div class="pt-item">
                              <span class="pt-label">端口</span>
                              <el-input v-model="ct.probes[pk].port" placeholder="数字或端口名" style="width: 150px" />
                            </div>
                          </template>
                          <template v-else>
                            <div class="pt-item" style="flex: 1">
                              <span class="pt-label">命令</span>
                              <el-input v-model="ct.probes[pk].command" placeholder="如 cat /tmp/healthy" />
                            </div>
                          </template>
                        </div>
                        <!-- 阈值网格：label 在上、控件在下（readiness 多一项成功阈值，自动换行） -->
                        <div v-if="ct.probes[pk].type !== 'none'" class="probe-thresholds">
                          <div class="res-item"><span>启动延迟（秒）</span>
                            <el-input-number v-model="ct.probes[pk].initialDelay" :min="0" :max="3600" controls-position="right" style="width: 100%" />
                          </div>
                          <div class="res-item"><span>间隔（秒）</span>
                            <el-input-number v-model="ct.probes[pk].period" :min="1" :max="3600" controls-position="right" style="width: 100%" />
                          </div>
                          <div class="res-item"><span>超时（秒）</span>
                            <el-input-number v-model="ct.probes[pk].timeout" :min="1" :max="3600" controls-position="right" style="width: 100%" />
                          </div>
                          <div class="res-item"><span>失败阈值（次）</span>
                            <el-input-number v-model="ct.probes[pk].failure" :min="1" :max="100" controls-position="right" style="width: 100%" />
                          </div>
                          <div v-if="pk === 'readiness'" class="res-item"><span>成功阈值（次）</span>
                            <el-input-number v-model="ct.probes[pk].success" :min="1" :max="100" controls-position="right" style="width: 100%" />
                          </div>
                        </div>
                        <div class="dim probe-desc">{{ probeMeta[pk].desc }}</div>
                      </div>
                      <div class="dim" style="font-size: 12px; margin-top: 2px">
                        修改探针会更新 Pod 模板，保存后触发滚动重启（Pod 逐个重建）。K8s 默认：间隔 10s / 超时 1s / 失败阈值 3 次。
                      </div>
                    </template>
                    <template v-else-if="formTab === 'storage'">
                      <el-table :data="ct.mounts" border>
                        <el-table-column label="卷类型" width="160">
                          <template #default="{ row }">
                            <el-select v-model="row.type" @change="row.volName = ''">
                              <el-option value="pvc" label="持久卷 PVC" />
                              <el-option value="configmap" label="ConfigMap" />
                              <el-option value="secret" label="Secret" />
                              <el-option value="emptydir" label="临时卷 emptyDir" />
                            </el-select>
                          </template>
                        </el-table-column>
                        <el-table-column label="引用对象名" min-width="240">
                          <template #default="{ row }">
                            <div v-if="row.type !== 'emptydir'" class="vol-ref">
                              <el-select v-model="row.refName" filterable allow-create default-first-option
                                placeholder="选择或输入对象名" @focus="loadRefOptions(row.type, editNS())">
                                <el-option v-for="n in refOptsOf(row.type, editNS())" :key="n" :label="n" :value="n" />
                              </el-select>
                              <el-button v-if="row.type === 'pvc'" link type="primary" @click="openNewPvc(row)">新建</el-button>
                              <el-button v-else link type="primary" @click="openNewCfg(row, row.type)">新建</el-button>
                            </div>
                            <span v-else class="dim">随 Pod 生命周期，重启不丢、重建即清空</span>
                          </template>
                        </el-table-column>
                        <el-table-column label="挂载路径" min-width="190">
                          <template #default="{ row }"><el-input v-model="row.mountPath" placeholder="/data" /></template>
                        </el-table-column>
                        <el-table-column label="子路径" min-width="150">
                          <template #default="{ row }"><el-input v-model="row.subPath" placeholder="可选，卷内子目录/文件" /></template>
                        </el-table-column>
                        <el-table-column label="只读" width="70">
                          <template #default="{ row }"><el-switch v-model="row.readOnly" /></template>
                        </el-table-column>
                        <el-table-column label="操作" width="80">
                          <template #default="{ $index }"><el-button link type="danger" @click="ct.mounts.splice($index, 1)">删除</el-button></template>
                        </el-table-column>
                      </el-table>
                      <div style="margin-top: 10px">
                        <el-button @click="ct.mounts.push({ type: 'pvc', refName: '', mountPath: '', subPath: '', readOnly: false, volName: '' })">+ 添加挂载</el-button>
                      </div>
                      <div v-if="(ct.keptMounts || []).length" class="dim" style="margin-top: 6px; font-size: 12px">
                        另有 {{ ct.keptMounts.length }} 个本表单不支持的挂载（hostPath/projected 等），保存时保持原样；如需修改请用 YAML 编辑
                      </div>
                    </template>
                  </div>
                </template>
              </div>
              </el-tab-pane>
            </el-tabs>
          </el-tab-pane>
          <el-tab-pane :label="cur.resource === 'pods' ? 'YAML 预览' : 'YAML'" name="yaml" lazy>
            <div v-if="detailYaml" style="margin-bottom: 8px; display: flex; justify-content: flex-end">
              <!-- 只读 AI 解释：说明这段 YAML 的结构与潜在问题，不改动任何内容 -->
              <el-button size="small" :icon="MagicStick" @click="explainYaml">AI 解释</el-button>
            </div>
            <!-- AI 修改：按自然语言指令产出修改后的草稿，diff 确认后填入编辑器，保存仍走既有通道 -->
            <div v-if="detailYaml && yamlEditable" style="margin-bottom: 8px; display: flex; gap: 8px">
              <el-input v-model="aiEditPrompt" size="small" placeholder="AI 修改指令，如：把镜像换成 nginx:1.27，副本数改为 3"
                :disabled="aiEditing" @keyup.enter="doAiYamlEdit" />
              <el-button size="small" type="primary" plain :icon="MagicStick" :loading="aiEditing" @click="doAiYamlEdit">AI 修改</el-button>
            </div>
            <YamlEditor v-model="detailYaml" :read-only="cur.resource === 'pods' || !verbAllowed(authResKey, 'update', detailTarget?.metadata?.namespace)" />
            <el-alert v-if="cur.resource === 'pods'" type="info" :closable="false" style="margin-top: 8px"
              title="Pod 是运行实例，仅支持预览；如需修改请编辑所属的应用（Deployment 等）" />
            <el-alert v-else-if="isCR(cur.resource)" type="info" :closable="false" style="margin-top: 8px"
              title="自定义资源：仅支持 YAML 编辑；保存范围受授权中「可修改字段」控制（actions=* 即完整更新；填字段路径如 spec.image 仅该路径可改）。" />
          </el-tab-pane>
        </el-tabs>
        <div class="drawer-actions">
          <el-button @click="detailDlg = false">关闭</el-button>
          <template v-if="detailTab === 'form' && verbAllowed(authResKey, 'update', detailTarget?.metadata?.namespace) && sectionEditable(formTab)">
            <el-button :loading="staging" @click="stageFormSection">暂存修改</el-button>
            <el-button v-if="restartSections.includes(formTab)"
              type="primary" :loading="formSaving" @click="applyNow">立即更新</el-button>
            <el-button v-else type="primary" :loading="formSaving" @click="saveFormSection">保存{{ currentSectionLabel }}</el-button>
          </template>
          <el-button v-else-if="cur.resource !== 'pods' && verbAllowed(authResKey, 'update', detailTarget?.metadata?.namespace)" type="primary" :loading="saving" @click="saveDetail">保存修改</el-button>
        </div>
      </div>
    </el-drawer>

    <!-- 存储挂载：新建 PVC（存储类/容量/访问模式是 PVC 创建时的属性；创建后回填挂载行） -->
    <el-dialog v-model="pvcDlg" title="新建 PVC" width="520px" destroy-on-close>
      <el-form label-width="90px">
        <el-form-item v-if="pvcStandalone" label="命名空间" required>
          <el-select v-model="createNS" filterable placeholder="选择命名空间" style="width: 100%">
            <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="PVC 名称" required>
          <el-input v-model="pvcForm.name" placeholder="如 my-app-data" maxlength="253" />
        </el-form-item>
        <el-form-item label="存储类">
          <el-select v-model="pvcForm.storageClass" filterable allow-create default-first-option clearable
            placeholder="留空使用集群默认存储类">
            <el-option v-for="sc in scOptions" :key="sc" :label="sc" :value="sc" />
          </el-select>
        </el-form-item>
        <el-form-item label="容量" required>
          <el-input v-model="pvcForm.size" placeholder="10Gi" style="width: 180px" />
        </el-form-item>
        <el-form-item label="访问模式">
          <el-select v-model="pvcForm.accessMode" style="width: 300px">
            <el-option value="ReadWriteOnce" label="ReadWriteOnce（单节点读写）" />
            <el-option value="ReadOnlyMany" label="ReadOnlyMany（多节点只读）" />
            <el-option value="ReadWriteMany" label="ReadWriteMany（多节点读写）" />
            <el-option value="ReadWriteOncePod" label="ReadWriteOncePod（单 Pod 读写）" />
          </el-select>
        </el-form-item>
      </el-form>
      <div class="dim" style="font-size: 12px">
        创建 PVC 需要 persistentvolumeclaims 的 create 权限{{ pvcStandalone ? '' : '；创建成功后自动填入当前挂载行，仍需点「保存存储挂载」才会挂载到容器' }}
      </div>
      <template #footer>
        <el-button @click="pvcDlg = false">取消</el-button>
        <el-button type="primary" :loading="pvcCreating" @click="createPvc">{{ pvcStandalone ? '创建' : '创建并回填' }}</el-button>
      </template>
    </el-dialog>

    <!-- 存储挂载 / 列表页：新建 ConfigMap / Secret（key-value 明文编辑；Secret 用 stringData 创建，apiserver 自动编码） -->
    <el-dialog v-model="cfgDlg" :title="cfgForm.kind === 'configmap' ? '新建 ConfigMap' : '新建 Secret'" width="880px" top="4vh"
      class="cfg-create-dlg" destroy-on-close>
      <el-form label-width="90px">
        <div class="create-grid">
          <el-form-item v-if="cfgStandalone" label="命名空间" required>
            <el-select v-model="createNS" filterable placeholder="选择命名空间" style="width: 100%">
              <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
            </el-select>
          </el-form-item>
          <el-form-item label="名称" required>
            <el-input v-model="cfgForm.name" placeholder="如 my-app-conf" maxlength="253" />
          </el-form-item>
        </div>
      </el-form>
      <el-divider content-position="left">数据项（key-value）</el-divider>
      <el-table :data="cfgForm.entries" border>
        <el-table-column label="键 (key)" width="260">
          <template #default="{ row }"><el-input v-model="row.key" placeholder="如 config.yaml / DB_PASSWORD" /></template>
        </el-table-column>
        <el-table-column label="值 (value)" min-width="480">
          <template #default="{ row }"><el-input v-model="row.value" type="textarea" :autosize="{ minRows: 3, maxRows: 12 }" placeholder="配置内容（明文）；多行文本、YAML / JSON / properties 均可" /></template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ $index }"><el-button link type="danger" @click="cfgForm.entries.splice($index, 1)">删除</el-button></template>
        </el-table-column>
      </el-table>
      <el-button style="margin-top: 10px" @click="cfgForm.entries.push({ key: '', value: '' })">+ 添加数据项</el-button>
      <div class="dim" style="font-size: 12px; margin-top: 6px">
        创建需要{{ cfgForm.kind === 'configmap' ? ' configmaps' : ' secrets' }} 的 create 权限；Secret 以明文填写、保存后自动 base64 编码{{ cfgStandalone ? '' : '；创建成功自动填入当前挂载行，仍需点「保存存储挂载」才会挂载到容器' }}
      </div>
      <template #footer>
        <el-button @click="cfgDlg = false">取消</el-button>
        <el-button type="primary" :loading="cfgCreating" @click="createCfg">{{ cfgStandalone ? '创建' : '创建并回填' }}</el-button>
      </template>
    </el-dialog>

    <!-- 列表页白屏创建：Service -->
    <el-dialog v-model="svcCreateDlg" title="新建 Service" width="780px" top="5vh" class="cfg-create-dlg" destroy-on-close>
      <el-form label-width="90px">
        <div class="create-grid">
          <el-form-item label="命名空间" required>
            <el-select v-model="createNS" filterable placeholder="选择命名空间" style="width: 100%">
              <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
            </el-select>
          </el-form-item>
          <el-form-item label="名称" required>
            <el-input v-model="svcCreateForm.name" placeholder="如 my-app-svc" maxlength="253" />
          </el-form-item>
          <el-form-item label="类型">
            <el-select v-model="svcCreateForm.type" style="width: 100%">
              <el-option value="ClusterIP" label="ClusterIP（集群内访问）" />
              <el-option value="NodePort" label="NodePort（节点端口暴露）" />
              <el-option value="LoadBalancer" label="LoadBalancer（负载均衡）" />
            </el-select>
          </el-form-item>
        </div>
      </el-form>
      <el-divider content-position="left">端口</el-divider>
      <el-table :data="svcCreateForm.ports" border>
        <el-table-column label="名称" min-width="130"><template #default="{ row }"><el-input v-model="row.name" placeholder="可选" /></template></el-table-column>
        <el-table-column label="端口" width="150"><template #default="{ row }"><el-input-number v-model="row.port" :min="1" :max="65535" controls-position="right" style="width: 130px" /></template></el-table-column>
        <el-table-column label="目标端口" width="150"><template #default="{ row }"><el-input v-model="row.targetPort" placeholder="留空同端口" /></template></el-table-column>
        <el-table-column label="协议" width="110"><template #default="{ row }"><el-select v-model="row.protocol"><el-option value="TCP" /><el-option value="UDP" /></el-select></template></el-table-column>
        <el-table-column label="操作" width="80"><template #default="{ $index }"><el-button link type="danger" @click="svcCreateForm.ports.splice($index, 1)">删除</el-button></template></el-table-column>
      </el-table>
      <el-button style="margin-top: 10px" @click="svcCreateForm.ports.push({ name: '', port: 80, targetPort: '', protocol: 'TCP' })">+ 添加端口</el-button>
      <el-divider content-position="left">选择器（关联哪些 Pod）</el-divider>
      <el-table :data="svcCreateForm.selector" border>
        <el-table-column label="Key" min-width="180"><template #default="{ row }"><el-input v-model="row.key" placeholder="如 app" /></template></el-table-column>
        <el-table-column label="Value" min-width="220"><template #default="{ row }"><el-input v-model="row.value" placeholder="如 my-app（需与 Pod 的 labels 一致）" /></template></el-table-column>
        <el-table-column label="操作" width="80"><template #default="{ $index }"><el-button link type="danger" @click="svcCreateForm.selector.splice($index, 1)">删除</el-button></template></el-table-column>
      </el-table>
      <el-button style="margin-top: 10px" @click="svcCreateForm.selector.push({ key: '', value: '' })">+ 添加标签</el-button>
      <div class="dim" style="font-size: 12px; margin-top: 6px">
        创建需要 services 的 create 权限；选择器需与 Pod 的 labels 匹配才能转发流量
      </div>
      <template #footer>
        <el-button @click="svcCreateDlg = false">取消</el-button>
        <el-button type="primary" :loading="svcCreating" @click="createSvc">创建</el-button>
      </template>
    </el-dialog>

    <!-- 列表页白屏创建：Ingress（行式路由，同域名自动归组） -->
    <el-dialog v-model="ingCreateDlg" title="新建 Ingress" width="940px" top="5vh" class="cfg-create-dlg" destroy-on-close>
      <el-form label-width="90px">
        <div class="create-grid">
          <el-form-item label="命名空间" required>
            <el-select v-model="createNS" filterable placeholder="选择命名空间" style="width: 100%" @change="loadSvcNames">
              <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
            </el-select>
          </el-form-item>
          <el-form-item label="名称" required>
            <el-input v-model="ingCreateForm.name" placeholder="如 my-app-ingress" maxlength="253" />
          </el-form-item>
          <el-form-item label="Ingress 类">
            <el-input v-model="ingCreateForm.ingressClass" placeholder="如 nginx；留空用集群默认" />
          </el-form-item>
        </div>
      </el-form>
      <el-divider content-position="left">路由规则</el-divider>
      <el-table :data="ingCreateForm.rows" border>
        <el-table-column label="域名" min-width="200">
          <template #default="{ row }"><el-input v-model="row.host" placeholder="app.example.com" /></template>
        </el-table-column>
        <el-table-column label="路径" width="140">
          <template #default="{ row }"><el-input v-model="row.path" placeholder="/" /></template>
        </el-table-column>
        <el-table-column label="路径类型" width="180">
          <template #default="{ row }">
            <el-select v-model="row.pathType">
              <el-option value="Prefix" label="Prefix（前缀匹配）" />
              <el-option value="Exact" label="Exact（精确匹配）" />
              <el-option value="ImplementationSpecific" label="ImplementationSpecific" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="后端服务" min-width="190">
          <template #default="{ row }">
            <el-select v-model="row.svc" filterable allow-create default-first-option placeholder="选择或输入服务名">
              <el-option v-for="n in svcNameOptions" :key="n" :label="n" :value="n" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="服务端口" width="140">
          <template #default="{ row }"><el-input-number v-model="row.port" :min="1" :max="65535" controls-position="right" style="width: 120px" /></template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ $index }"><el-button link type="danger" @click="ingCreateForm.rows.splice($index, 1)">删除</el-button></template>
        </el-table-column>
      </el-table>
      <el-button style="margin-top: 10px" @click="ingCreateForm.rows.push({ host: '', path: '/', pathType: 'Prefix', svc: '', port: 80 })">+ 添加路由</el-button>
      <div class="dim" style="font-size: 12px; margin-top: 6px">
        创建需要 ingresses 的 create 权限；同一域名的多行自动合并为一条规则；后端服务需已存在同命名空间
      </div>
      <template #footer>
        <el-button @click="ingCreateDlg = false">取消</el-button>
        <el-button type="primary" :loading="ingCreating" @click="createIng">创建</el-button>
      </template>
    </el-dialog>

    <!-- YAML 部署对话框 -->
    <el-dialog v-model="deployDlg" title="YAML 部署" width="60%" top="5vh" destroy-on-close>
      <el-alert v-for="(r, i) in deployResults" :key="i" :title="`${r.kind}/${r.name}（${r.namespace}）：${r.message}`"
        :type="r.ok ? 'success' : 'error'" :closable="true" style="margin-bottom: 10px" />
      <el-form label-width="90px">
        <el-form-item label="命名空间">
          <el-select v-model="deployNS" style="width: 220px" filterable placeholder="留空使用 YAML 内 namespace">
            <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="AI 生成">
          <div style="width: 100%; display: flex; gap: 8px">
            <el-input v-model="aiGenPrompt" placeholder="用自然语言描述要部署的应用，如：部署 2 副本的 nginx，80 端口，限制 1C1G"
              @keyup.enter="genYaml" :disabled="aiGenerating" />
            <el-button :icon="MagicStick" :loading="aiGenerating" @click="genYaml">生成草稿</el-button>
          </div>
          <div class="dim" style="font-size: 12px; width: 100%; margin-top: 4px">
            AI 生成的是草稿，会填入下方编辑器，请人工审阅修改后再部署（部署仍走权限校验与审计）
          </div>
        </el-form-item>
        <el-form-item label="YAML">
          <div class="deploy-editor">
            <YamlEditor v-model="deployYaml" />
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="deployDlg = false">关闭</el-button>
        <!-- 部署前快速风险审查（只读）：latest 镜像/缺资源限制/探针/危险配置等 -->
        <el-button :disabled="!deployYaml.trim()" :loading="reviewing" @click="reviewYaml">
          {{ reviewing ? '审查中…' : 'AI 审查' }}
        </el-button>
        <el-button :loading="deploying" @click="doApply(true)">试运行</el-button>
        <el-button type="primary" :loading="deploying" @click="doApply(false)">部署</el-button>
      </template>
    </el-dialog>

    <!-- 白屏部署（表单向导，适合不熟悉 YAML 的用户） -->
    <el-dialog v-model="fdDlg" title="白屏部署（表单创建应用）" width="60%" top="3vh" destroy-on-close>
      <el-alert v-for="(r, i) in fdResults" :key="i" :title="`${r.kind}/${r.name}（${r.namespace}）：${r.message}`"
        :type="r.ok ? 'success' : 'error'" :closable="true" style="margin-bottom: 10px" />
      <el-form label-width="92px" class="fd-form">
        <el-divider content-position="left">基本信息</el-divider>
        <div class="fd-section">
        <el-form-item label="容器类型" required>
          <el-radio-group v-if="fdKindOptions.length" v-model="fd.kind">
            <el-radio-button v-for="k in fdKindOptions" :key="k.value" :value="k.value">{{ k.label }}</el-radio-button>
          </el-radio-group>
          <el-alert v-else type="info" :closable="false"
            title="当前授权不含任何工作负载类型的 create 权限，无法使用白屏部署" />
        </el-form-item>
        <div class="fd-grid">
          <el-form-item label="名称" required>
            <el-input v-model="fd.name" placeholder="小写字母/数字/中划线，如 my-app" />
          </el-form-item>
          <el-form-item label="命名空间" required>
            <el-select v-model="fd.namespace" filterable placeholder="选择命名空间">
              <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="fd.kind === 'deployment' || fd.kind === 'statefulset'" label="容器副本">
            <el-input-number v-model="fd.replicas" :min="0" :max="99" />
          </el-form-item>
          <el-form-item v-if="fd.kind === 'job'" label="完成数">
            <el-input-number v-model="fd.completions" :min="1" :max="99" />
          </el-form-item>
          <el-form-item v-if="fd.kind === 'cronjob'" label="Cron 表达式">
            <el-input v-model="fd.schedule" placeholder="*/5 * * * *（分 时 日 月 周）" style="width: 220px" />
          </el-form-item>
        </div>
        </div>

        <template v-for="(ct, i) in fd.containers" :key="i">
          <div class="fd-ct">
            <div class="fd-ct-head">
              <span class="fd-ct-title">容器 {{ i + 1 }}<span v-if="ct.name" class="dim"> · {{ ct.name }}</span></span>
              <el-button v-if="fd.containers.length > 1" link type="danger" size="small"
                @click="fd.containers.splice(i, 1)">移除容器</el-button>
            </div>
            <div class="fd-ct-body">
          <div class="fd-grid fd-grid-main">
            <el-form-item label="容器名" required><el-input v-model="ct.name" placeholder="main" /></el-form-item>
            <el-form-item label="容器镜像" required><el-input v-model="ct.image" placeholder="registry.example.com/namespace/app:v1.2.3（建议固定 tag，不指定将拉取 latest）" clearable /></el-form-item>
          </div>

          <!-- 其余配置默认折叠（90% 场景只填名称+镜像）；标题实时显示已配置摘要 -->
          <el-collapse v-model="ct.open" class="fd-collapse">
          <el-collapse-item name="cmd">
            <template #title><div class="fd-col-title"><span>入口 / 命令（Entrypoint / Command）</span><span class="fd-col-sum">{{ fdCmdSum(ct) }}</span></div></template>
          <el-form-item label="入口 (Entrypoint)">
            <el-input v-model="ct.command" placeholder="例如: /bin/sh；留空 = 镜像默认（此时命令将自动用 /bin/sh -c 执行）" />
          </el-form-item>
          <el-form-item label="命令">
            <el-input v-model="ct.args" type="textarea" :autosize="{ minRows: 2, maxRows: 8 }"
              placeholder="直接输入要执行的命令，多行 = 多条依序执行（无需写 -c，也无需引号），如&#10;kubectl delete pod --field-selector=status.phase=Succeeded&#10;echo done" />
          </el-form-item>
          </el-collapse-item>
          <el-collapse-item name="ports">
            <template #title><div class="fd-col-title"><span>端口</span><span class="fd-col-sum">{{ fdPortsSum(ct) }}</span></div></template>
          <el-form-item label="端口">
            <el-table :data="ct.ports" size="small" border class="fd-table">
              <el-table-column label="名称" min-width="90">
                <template #default="{ row }"><el-input v-model="row.name" placeholder="http" /></template>
              </el-table-column>
              <el-table-column label="容器端口" min-width="100">
                <template #default="{ row }"><el-input-number v-model="row.containerPort" :min="1" :max="65535" controls-position="right" style="width: 100%" /></template>
              </el-table-column>
              <el-table-column label="主机端口" min-width="100">
                <template #default="{ row }"><el-input-number v-model="row.hostPort" :min="1" :max="65535" controls-position="right" placeholder="可选" style="width: 100%" /></template>
              </el-table-column>
              <el-table-column label="协议" min-width="90">
                <template #default="{ row }">
                  <el-select v-model="row.protocol"><el-option value="TCP" /><el-option value="UDP" /><el-option value="SCTP" /></el-select>
                </template>
              </el-table-column>
              <el-table-column width="60">
                <template #default="{ $index }">
                  <el-button link type="danger" size="small" @click="ct.ports.splice($index, 1)">删</el-button>
                </template>
              </el-table-column>
            </el-table>
            <el-button size="small" style="margin-top: 6px" @click="ct.ports.push({ name: '', containerPort: 80, hostPort: null, protocol: 'TCP' })">+ 添加端口</el-button>
          </el-form-item>
          </el-collapse-item>
          <el-collapse-item name="res">
            <template #title><div class="fd-col-title"><span>资源配置</span><span class="fd-col-sum">{{ fdResSum(ct) }}</span></div></template>
          <el-form-item label="容器资源">
            <div class="fd-res">
              <div class="fd-grid fd-res-grid">
                <div class="fd-field"><span class="dim">CPU 请求</span>
                  <div class="qty-row">
                    <el-input v-model="ct.res.reqCpu.num" placeholder="如 0.5" />
                    <el-select v-model="ct.res.reqCpu.unit" class="qty-unit">
                      <el-option value="m" label="m 毫核" /><el-option value="core" label="c（核）" />
                    </el-select>
                  </div>
                </div>
                <div class="fd-field"><span class="dim">内存请求</span>
                  <div class="qty-row">
                    <el-input v-model="ct.res.reqMem.num" placeholder="如 1" />
                    <el-select v-model="ct.res.reqMem.unit" class="qty-unit">
                      <el-option value="Mi" label="Mi" /><el-option value="Gi" label="Gi" /><el-option value="Ki" label="Ki" />
                    </el-select>
                  </div>
                </div>
                <div class="fd-field"><span class="dim">CPU 上限</span>
                  <div class="qty-row">
                    <el-input v-model="ct.res.limitsCpu.num" placeholder="如 1" />
                    <el-select v-model="ct.res.limitsCpu.unit" class="qty-unit">
                      <el-option value="m" label="m 毫核" /><el-option value="core" label="c（核）" />
                    </el-select>
                  </div>
                </div>
                <div class="fd-field"><span class="dim">内存上限</span>
                  <div class="qty-row">
                    <el-input v-model="ct.res.limitsMem.num" placeholder="如 2" />
                    <el-select v-model="ct.res.limitsMem.unit" class="qty-unit">
                      <el-option value="Mi" label="Mi" /><el-option value="Gi" label="Gi" /><el-option value="Ki" label="Ki" />
                    </el-select>
                  </div>
                </div>
              </div>
              <div v-if="fdResourceBoundsError(ct)" class="resource-validation-error" role="alert">
                {{ fdResourceBoundsError(ct) }}
              </div>
              <div class="dim" style="font-size: 12px; margin: 4px 0">异构资源（GPU / NPU 等，计入上限）：</div>
              <el-table :data="ct.res.extended" size="small" border class="fd-table">
                <el-table-column label="资源名" min-width="220">
                  <template #default="{ row }">
                    <el-input v-model="row.key" placeholder="nvidia.com/gpu / huawei.com/npu" />
                  </template>
                </el-table-column>
                <el-table-column label="数量" min-width="110">
                  <template #default="{ row }"><el-input v-model="row.value" placeholder="1" /></template>
                </el-table-column>
                <el-table-column width="60">
                  <template #default="{ $index }">
                    <el-button link type="danger" size="small" @click="ct.res.extended.splice($index, 1)">删</el-button>
                  </template>
                </el-table-column>
              </el-table>
              <el-button size="small" style="margin-top: 6px" @click="ct.res.extended.push({ key: 'nvidia.com/gpu', value: '1' })">+ 添加异构资源</el-button>
            </div>
          </el-form-item>
          </el-collapse-item>
          <el-collapse-item name="env">
            <template #title><div class="fd-col-title"><span>环境变量</span><span class="fd-col-sum">{{ fdEnvSum(ct) }}</span></div></template>
          <el-form-item label="环境变量">
            <el-table :data="ct.env" size="small" border class="fd-table">
              <el-table-column label="名称" min-width="180">
                <template #default="{ row }"><el-input v-model="row.name" placeholder="KEY" /></template>
              </el-table-column>
              <el-table-column label="来源" min-width="120">
                <template #default="{ row }">
                  <el-select v-model="row.type">
                    <el-option value="value" label="直接值" />
                    <el-option value="secret" label="Secret" />
                    <el-option value="configmap" label="ConfigMap" />
                    <el-option value="fieldref" label="字段引用" />
                  </el-select>
                </template>
              </el-table-column>
              <el-table-column label="值 / 引用" min-width="380">
                <template #default="{ row }">
                  <template v-if="row.type === 'value'"><el-input v-model="row.value" placeholder="value" /></template>
                  <template v-else-if="row.type === 'fieldref'">
                    <el-input v-model="row.refKey" placeholder="字段路径，如 metadata.name" />
                  </template>
                  <template v-else>
                    <div class="fd-grid">
                      <el-select v-model="row.refName" filterable allow-create default-first-option
                        placeholder="引用名" style="width: 100%"
                        @focus="loadRefOptions(row.type, fd.namespace)"
                        @change="row.refKey = ''; loadEnvKeys(row.type, fd.namespace, row.refName)">
                        <el-option v-for="n in refOptsOf(row.type, fd.namespace)" :key="n" :label="n" :value="n" />
                      </el-select>
                      <el-select v-model="row.refKey" filterable allow-create default-first-option
                        :placeholder="row.refName ? '键' : '先选引用名'" style="width: 100%"
                        @focus="loadEnvKeys(row.type, fd.namespace, row.refName)">
                        <el-option v-for="k in envKeyOpts(row.type, fd.namespace, row.refName)" :key="k" :label="k" :value="k" />
                      </el-select>
                    </div>
                  </template>
                </template>
              </el-table-column>
              <el-table-column width="60">
                <template #default="{ $index }">
                  <el-button link type="danger" size="small" @click="ct.env.splice($index, 1)">删</el-button>
                </template>
              </el-table-column>
            </el-table>
            <el-button size="small" style="margin-top: 6px" @click="ct.env.push({ name: '', type: 'value', value: '', refName: '', refKey: '' })">+ 添加环境变量</el-button>
          </el-form-item>
          </el-collapse-item>
          <el-collapse-item name="probes">
            <template #title><div class="fd-col-title"><span>健康检查</span><span class="fd-col-sum">{{ fdProbeSum(ct) }}</span></div></template>
          <el-form-item label="健康检查">
            <div class="fd-probe">
              <div class="fd-probe-row">
                <span class="dim" style="width: 70px">存活检查</span>
                <el-select v-model="ct.liveness.type" style="width: 130px">
                  <el-option value="none" label="不启用" />
                  <el-option value="http" label="HTTP 请求" />
                  <el-option value="tcp" label="TCP 端口" />
                  <el-option value="exec" label="执行命令" />
                </el-select>
                <template v-if="ct.liveness.type === 'http'">
                  <el-input v-model="ct.liveness.path" placeholder="路径 /healthz" style="width: 160px" />
                  <el-input v-model="ct.liveness.port" placeholder="端口 8080" style="width: 110px" />
                  <el-select v-model="ct.liveness.scheme" style="width: 92px">
                    <el-option value="HTTP" label="HTTP" />
                    <el-option value="HTTPS" label="HTTPS" />
                  </el-select>
                </template>
                <el-input v-else-if="ct.liveness.type === 'tcp'" v-model="ct.liveness.port" placeholder="端口 8080" style="width: 110px" />
                <el-input v-else-if="ct.liveness.type === 'exec'" v-model="ct.liveness.command" placeholder="命令，如 cat /tmp/healthy" style="width: 300px" />
              </div>
              <div class="fd-probe-row" v-if="ct.liveness.type !== 'none'">
                <span class="dim" style="width: 70px"></span>
                <span class="dim">延迟</span><el-input-number v-model="ct.liveness.initialDelay" :min="0" :max="600" size="small" controls-position="right" />
                <span class="dim">间隔</span><el-input-number v-model="ct.liveness.period" :min="1" :max="300" size="small" controls-position="right" />
              </div>
              <div class="fd-probe-row">
                <span class="dim" style="width: 70px">就绪检查</span>
                <el-select v-model="ct.readiness.type" style="width: 130px">
                  <el-option value="none" label="不启用" />
                  <el-option value="http" label="HTTP 请求" />
                  <el-option value="tcp" label="TCP 端口" />
                  <el-option value="exec" label="执行命令" />
                </el-select>
                <template v-if="ct.readiness.type === 'http'">
                  <el-input v-model="ct.readiness.path" placeholder="路径 /ready" style="width: 160px" />
                  <el-input v-model="ct.readiness.port" placeholder="端口 8080" style="width: 110px" />
                  <el-select v-model="ct.readiness.scheme" style="width: 92px">
                    <el-option value="HTTP" label="HTTP" />
                    <el-option value="HTTPS" label="HTTPS" />
                  </el-select>
                </template>
                <el-input v-else-if="ct.readiness.type === 'tcp'" v-model="ct.readiness.port" placeholder="端口 8080" style="width: 110px" />
                <el-input v-else-if="ct.readiness.type === 'exec'" v-model="ct.readiness.command" placeholder="命令" style="width: 300px" />
              </div>
              <div class="fd-probe-row" v-if="ct.readiness.type !== 'none'">
                <span class="dim" style="width: 70px"></span>
                <span class="dim">延迟</span><el-input-number v-model="ct.readiness.initialDelay" :min="0" :max="600" size="small" controls-position="right" />
                <span class="dim">间隔</span><el-input-number v-model="ct.readiness.period" :min="1" :max="300" size="small" controls-position="right" />
              </div>
            </div>
          </el-form-item>
          </el-collapse-item>
          </el-collapse>
            </div>
          </div>
        </template>
        <el-button size="small" style="margin: 0 0 14px 8px" @click="fd.containers.push(fdNewContainer())">+ 添加容器（边车）</el-button>

        <div class="fd-advanced-entry">
          <div class="fd-advanced-copy">
            <strong>可选配置</strong>
            <span>需要持久化存储、服务访问或调度策略（节点选择 / 亲和）时，在此配置。</span>
          </div>
          <div class="fd-advanced-control">
            <span>显示配置项</span>
            <el-switch
              class="fd-advanced-switch"
              :model-value="fdAdvOpen.includes('adv')"
              active-text="已展开"
              inactive-text="展开"
              aria-label="展开或收起存储与 Service 可选配置"
              aria-controls="fd-advanced-options"
              @change="toggleFdAdvanced"
            />
          </div>
        </div>
        <el-collapse v-model="fdAdvOpen" class="fd-collapse">
        <el-collapse-item name="adv">
          <template #title><div class="fd-col-title"><span>存储 / Service / 调度策略</span><span class="fd-col-sum">{{ fdAdvSum() }}</span></div></template>
        <div id="fd-advanced-options">
        <el-alert type="info" :closable="false" class="fd-advanced-info"
          :title="`${fd.kind === 'statefulset' ? 'StatefulSet 将通过 volumeClaimTemplates 为每个副本分别申请 PVC。' : '将创建 PVC 并挂载到工作负载；多副本可能共用同一个 PVC，请确认访问模式和存储能力。'} 启用 Service 会额外创建同名 Service。`" />
        <el-form-item label="启用存储">
          <el-switch v-model="fd.storage.enabled" @change="fdOnStorage" />
          <span class="dim" style="margin-left: 10px; font-size: 12px">
            {{ fd.kind === 'statefulset' ? 'StatefulSet 使用 volumeClaimTemplates，每副本独立卷' : '创建独立 PVC 挂载到容器' }}
          </span>
        </el-form-item>
        <div class="fd-grid" v-if="fd.storage.enabled">
          <el-form-item label="存储类">
            <el-select v-model="fd.storage.storageClass" filterable allow-create default-first-option placeholder="默认存储类可留空">
              <el-option v-for="sc in scOptions" :key="sc" :label="sc" :value="sc" />
            </el-select>
          </el-form-item>
          <el-form-item label="存储卷容量">
            <el-input v-model="fd.storage.size" placeholder="10Gi" style="width: 160px" />
          </el-form-item>
          <el-form-item label="访问模式">
            <el-select v-model="fd.storage.accessMode" style="width: 220px">
              <el-option value="ReadWriteOnce" label="ReadWriteOnce（单节点读写）" />
              <el-option value="ReadOnlyMany" label="ReadOnlyMany（多节点只读）" />
              <el-option value="ReadWriteMany" label="ReadWriteMany（多节点读写）" />
              <el-option value="ReadWriteOncePod" label="ReadWriteOncePod（单 Pod 读写）" />
            </el-select>
          </el-form-item>
          <el-form-item label="挂载路径">
            <el-input v-model="fd.storage.mountPath" placeholder="/data" />
          </el-form-item>
        </div>
        <el-divider content-position="left" style="margin: 4px 0 14px">Service（服务访问）</el-divider>
        <el-form-item label="创建 Service">
          <el-switch v-model="fd.service.enabled" @change="fdOnService" />
        </el-form-item>
        <template v-if="fd.service.enabled">
          <div class="fd-grid">
            <el-form-item label="类型">
              <el-select v-model="fd.service.type" style="width: 260px">
                <el-option value="ClusterIP" label="ClusterIP（集群内访问）" />
                <el-option value="NodePort" label="NodePort（节点端口暴露）" />
                <el-option value="LoadBalancer" label="LoadBalancer（负载均衡器）" />
              </el-select>
            </el-form-item>
          </div>
          <el-form-item label="端口映射">
            <el-table :data="fd.service.ports" size="small" border class="fd-table">
              <el-table-column label="名称" min-width="90">
                <template #default="{ row }"><el-input v-model="row.name" placeholder="http" /></template>
              </el-table-column>
              <el-table-column label="服务端口" min-width="110">
                <template #default="{ row }"><el-input-number v-model="row.port" :min="1" :max="65535" controls-position="right" style="width: 100%" /></template>
              </el-table-column>
              <el-table-column label="容器端口" min-width="110">
                <template #default="{ row }"><el-input v-model="row.targetPort" :min="1" :max="65535" controls-position="right" style="width: 100%" /></template>
              </el-table-column>
              <el-table-column v-if="fd.service.type === 'NodePort'" label="节点端口" min-width="110">
                <template #default="{ row }"><el-input-number v-model="row.nodePort" :min="30000" :max="32767" controls-position="right" style="width: 100%" /></template>
              </el-table-column>
              <el-table-column width="60">
                <template #default="{ $index }">
                  <el-button link type="danger" size="small" @click="fd.service.ports.splice($index, 1)">删</el-button>
                </template>
              </el-table-column>
            </el-table>
            <div style="margin-top: 6px">
              <el-button size="small" @click="fd.service.ports.push({ name: '', port: 80, targetPort: 80, nodePort: null })">+ 添加端口</el-button>
              <el-button size="small" link type="primary" @click="fdFillSvcPorts">从容器端口填充</el-button>
            </div>
          </el-form-item>
        </template>
        <el-divider content-position="left" style="margin: 4px 0 14px">调度策略（节点选择 / 亲和）</el-divider>
        <SchedEditor v-model="fd.sched" :nodes="nodeOptions" :label-keys="nodeLabelKeys" :label-values="nodeLabelValues" />
        </div>
        </el-collapse-item>
        </el-collapse>
      </el-form>
      <template #footer>
        <el-button @click="fdDlg = false">关闭</el-button>
        <el-button @click="fdYamlDlg = true">查看 YAML</el-button>
        <el-button :loading="fdDeploying" :disabled="!fdKindOptions.length" @click="doFormDeploy(true)">试运行</el-button>
        <el-button type="primary" :loading="fdDeploying" :disabled="!fdKindOptions.length" @click="doFormDeploy(false)">部署</el-button>
      </template>
    </el-dialog>

    <!-- 白屏部署：HPA -->
    <el-dialog v-model="hpaDeployDlg" title="白屏部署（自动扩缩容 HPA）" width="46%" top="6vh" destroy-on-close>
      <el-alert v-for="(r, i) in hpaDeployResults" :key="i"
        :title="`${r.kind}/${r.name}（${r.namespace}）：${r.message}`"
        :type="r.ok ? 'success' : 'error'" :closable="true" style="margin-bottom: 10px" />
      <el-form label-width="108px" class="fd-form">
        <el-divider content-position="left">基本信息</el-divider>
        <div class="fd-grid">
          <el-form-item label="名称" required>
            <el-input v-model="hpaDeployForm.name" placeholder="my-app-hpa" />
          </el-form-item>
          <el-form-item label="命名空间" required>
            <el-select v-model="hpaDeployForm.namespace" filterable placeholder="选择命名空间" @change="loadHpaTargets">
              <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
            </el-select>
          </el-form-item>
        </div>
        <el-divider content-position="left">目标工作负载</el-divider>
        <div class="fd-grid">
          <el-form-item label="类型" required>
            <el-select v-model="hpaDeployForm.targetKind" @change="loadHpaTargets">
              <el-option value="Deployment" label="应用 (Deployment)" />
              <el-option value="StatefulSet" label="有状态应用 (StatefulSet)" />
            </el-select>
          </el-form-item>
          <el-form-item label="目标名称" required>
            <el-select v-model="hpaDeployForm.targetName" filterable allow-create default-first-option
              placeholder="选择或输入工作负载名称">
              <el-option v-for="t in hpaTargetOptions" :key="t" :label="t" :value="t" />
            </el-select>
          </el-form-item>
        </div>
        <el-divider content-position="left">副本范围</el-divider>
        <div class="fd-grid">
          <el-form-item label="最小副本" required>
            <el-input-number v-model="hpaDeployForm.minReplicas" :min="0" :max="99" />
          </el-form-item>
          <el-form-item label="最大副本" required>
            <el-input-number v-model="hpaDeployForm.maxReplicas" :min="1" :max="99" />
          </el-form-item>
        </div>
        <el-divider content-position="left">指标</el-divider>
        <div class="fd-grid">
          <el-form-item label="CPU 阈值">
            <el-input-number v-model="hpaDeployForm.cpu" :min="1" :max="100" />
          </el-form-item>
          <el-form-item label="内存阈值">
            <el-input-number v-model="hpaDeployForm.memory" :min="0" :max="100" />
          </el-form-item>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="hpaDeployDlg = false">关闭</el-button>
        <el-button @click="hpaYamlDlg = true">查看 YAML</el-button>
        <el-button :loading="hpaDeploying" @click="doHpaDeploy(true)">试运行</el-button>
        <el-button type="primary" :loading="hpaDeploying" @click="doHpaDeploy(false)">部署</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="hpaYamlDlg" title="YAML 预览（随表单实时生成，尚未部署）" width="46%" top="5vh" destroy-on-close>
      <div class="fd-preview">
        <YamlEditor :model-value="hpaYaml" read-only />
      </div>
      <template #footer>
        <el-button @click="hpaYamlDlg = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 白屏部署：ResourceQuota -->
    <el-dialog v-model="quotaDeployDlg" title="白屏部署（资源配额 ResourceQuota）" width="52%" top="5vh" destroy-on-close>
      <el-alert v-for="(r, i) in quotaDeployResults" :key="i"
        :title="`${r.kind}/${r.name}（${r.namespace}）：${r.message}`"
        :type="r.ok ? 'success' : 'error'" :closable="true" style="margin-bottom: 10px" />
      <el-form label-width="108px" class="fd-form">
        <el-divider content-position="left">基本信息</el-divider>
        <div class="fd-grid">
          <el-form-item label="名称" required>
            <el-input v-model="quotaDeployForm.name" placeholder="team-quota" />
          </el-form-item>
          <el-form-item label="命名空间" required>
            <el-select v-model="quotaDeployForm.namespace" filterable placeholder="选择命名空间">
              <el-option v-for="ns in namespaces" :key="ns.name" :label="nsLabel(ns)" :value="ns.name" />
            </el-select>
          </el-form-item>
        </div>
        <el-divider content-position="left">配额项</el-divider>
        <el-table :data="quotaDeployForm.items" size="small" border class="fd-table">
          <el-table-column label="资源" min-width="190">
            <template #default="{ row }">
              <el-select v-model="row.key" filterable>
                <el-option v-for="q in quotaKeyOptions" :key="q.value" :value="q.value" :label="q.label" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="上限" min-width="150">
            <template #default="{ row }">
              <el-input v-model="row.value" placeholder="如 4、500m、2Gi" />
            </template>
          </el-table-column>
          <el-table-column width="60">
            <template #default="{ $index }">
              <el-button link type="danger" size="small" @click="quotaDeployForm.items.splice($index, 1)">删</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div style="margin-top: 6px">
          <el-button size="small" @click="addQuotaItem">+ 添加配额</el-button>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="quotaDeployDlg = false">关闭</el-button>
        <el-button @click="quotaYamlDlg = true">查看 YAML</el-button>
        <el-button :loading="quotaDeploying" @click="doQuotaDeploy(true)">试运行</el-button>
        <el-button type="primary" :loading="quotaDeploying" @click="doQuotaDeploy(false)">部署</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="quotaYamlDlg" title="YAML 预览（随表单实时生成，尚未部署）" width="46%" top="5vh" destroy-on-close>
      <div class="fd-preview">
        <YamlEditor :model-value="quotaYaml" read-only />
      </div>
      <template #footer>
        <el-button @click="quotaYamlDlg = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 白屏部署：YAML 实时预览（主表单不再常驻占位，按需弹窗查看） -->
    <el-dialog v-model="fdYamlDlg" title="YAML 预览（随表单实时生成，尚未部署）" width="60%" top="5vh" destroy-on-close>
      <div class="fd-preview">
        <YamlEditor :model-value="fdYaml" read-only />
      </div>
      <template #footer>
        <el-button @click="fdYamlDlg = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 日志对话框 -->
    <el-dialog v-model="logsDlg" width="75%" top="4vh" destroy-on-close>
      <template #header>
        <div class="ws-dialog-header">
          <span class="dlg-title">日志 - {{ logsTarget?.pod }}</span>
          <!-- 多容器才显示切换（单容器零噪音）；切换即按新容器重新拉流 -->
          <el-select v-if="logsTarget?.containers?.length > 1" v-model="logContainer" size="small"
            style="width: 220px" title="切换容器">
            <el-option v-for="c in logsTarget.containers" :key="c" :value="c" :label="`容器：${c}`" />
          </el-select>
        </div>
      </template>
      <div class="ws-dialog-body">
        <LogViewer v-if="logsDlg && logsTarget" v-bind="logsTarget" :container="logContainer" />
      </div>
    </el-dialog>

    <!-- 终端对话框 -->
    <el-dialog v-model="termDlg" width="75%" top="4vh" destroy-on-close>
      <template #header>
        <div class="ws-dialog-header">
          <span class="dlg-title">终端 - {{ termTarget?.pod }}</span>
          <!-- 多容器才显示切换；切换容器 = 新的 exec 会话（:key 重建组件，旧会话关闭） -->
          <el-select v-if="termTarget?.containers?.length > 1" v-model="termContainer" size="small"
            style="width: 220px" title="切换容器（新会话）">
            <el-option v-for="c in termTarget.containers" :key="c" :value="c" :label="`容器：${c}`" />
          </el-select>
        </div>
      </template>
      <div class="ws-dialog-body">
        <Terminal v-if="termDlg && termTarget" v-bind="termTarget" :container="termContainer" :key="termContainer" />
      </div>
    </el-dialog>

    <!-- 上次退出详情（exit code / 原因 / 消息） -->
    <el-dialog v-model="exitDlg" :title="`上次退出详情 - ${exitRow?.metadata?.name}`" width="72%" top="6vh">
      <el-alert v-if="!exitInfo.some((c) => c.hasTerm)" type="info" :closable="false" style="margin-bottom: 12px"
        title="没有容器异常退出的记录（容器未重启过，或上次是正常退出/被驱逐等无 terminated 记录）" />
      <el-table :data="exitInfo" border size="small">
        <el-table-column prop="name" label="容器" min-width="140" show-overflow-tooltip />
        <el-table-column label="就绪" width="70">
          <template #default="{ row }">
            <el-tag :type="row.ready ? 'success' : 'danger'" size="small">{{ row.ready ? '是' : '否' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="restarts" label="重启" width="70" />
        <el-table-column label="上次退出码" width="100">
          <template #default="{ row }">
            <template v-if="row.hasTerm">
              <el-tag :type="row.exitCode === 0 ? 'success' : 'danger'" size="small">{{ row.exitCode }}</el-tag>
            </template>
            <span v-else class="dim">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="退出原因" width="130" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.hasTerm">{{ row.reason }}</span>
            <span v-else class="dim">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="startedAt" label="开始时间" width="160" show-overflow-tooltip>
          <template #default="{ row }"><span v-if="row.hasTerm">{{ row.startedAt }}</span><span v-else class="dim">-</span></template>
        </el-table-column>
        <el-table-column prop="finishedAt" label="结束时间" width="160" show-overflow-tooltip>
          <template #default="{ row }"><span v-if="row.hasTerm">{{ row.finishedAt }}</span><span v-else class="dim">-</span></template>
        </el-table-column>
        <el-table-column prop="message" label="明细" min-width="220" show-overflow-tooltip>
          <template #default="{ row }"><span v-if="row.hasTerm">{{ row.message }}</span><span v-else class="dim">-</span></template>
        </el-table-column>
      </el-table>
      <p class="dim" style="margin: 10px 0 0">数据来自 K8s 容器状态 lastState.terminated（上次容器终止记录）；常见退出码：0 正常、137 被 SIGKILL（OOM/强杀）、143 被 SIGTERM。</p>
    </el-dialog>

    <!-- 镜像历史（Deployment→ReplicaSet / StatefulSet/DaemonSet→ControllerRevision） -->
    <el-dialog v-model="imgHistDlg" :title="`镜像历史 - ${detailTitle}`" width="68%" top="6vh">
      <el-table :data="imgHistItems" border size="small" v-loading="imgHistLoading">
        <el-table-column label="版本" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.current" type="success" size="small">当前 #{{ row.revision }}</el-tag>
            <span v-else>#{{ row.revision }}</span>
          </template>
        </el-table-column>
        <el-table-column label="变更时间" width="170">
          <template #default="{ row }">
            <span v-if="row.time">{{ new Date(row.time).toLocaleString('zh-CN', { hour12: false }) }}</span>
            <span v-else class="dim">—</span>
          </template>
        </el-table-column>
        <el-table-column label="容器镜像" min-width="380">
          <template #default="{ row }">
            <div v-if="!row.containers?.length && !row.initContainers?.length" class="dim">—</div>
            <div v-for="c in row.containers" :key="c.name" class="hist-img">
              <span class="dim">{{ c.name }}：</span><code>{{ c.image }}</code>
            </div>
            <div v-for="c in row.initContainers" :key="'i-' + c.name" class="hist-img">
              <span class="dim">init {{ c.name }}：</span><code>{{ c.image }}</code>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="110" align="center">
          <template #default="{ row }">
            <el-button link type="primary" size="small" :disabled="row.current" @click="applyImageHistory(row)">回填此版本</el-button>
          </template>
        </el-table-column>
      </el-table>
      <p class="dim" style="margin: 10px 0 0">回填仅替换同名容器的镜像，需在「镜像」页点保存才生效；历史深度由资源的 revisionHistoryLimit 控制。Job / CronJob 模板无原生版本记录，不提供历史。</p>
    </el-dialog>

    <!-- Describe（kubectl describe 风格纯文本） -->
    <el-dialog v-model="describeDlg" :title="`Describe - ${describeRow?.metadata?.name}`" width="70%" top="5vh">
      <div v-loading="describeLoading" class="describe-box">
        <pre v-if="describeText" class="mono">{{ describeText }}</pre>
      </div>
      <template #footer>
        <el-button v-if="describeText" @click="copyDescribe">复制</el-button>
        <el-button type="primary" @click="describeDlg = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 容器文件浏览器（勾选下载） -->
    <el-dialog v-model="fileDlg" :title="`文件下载 - ${fileRow?.metadata?.name}`" width="60%" top="5vh">
      <div style="display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap">
        <el-select v-model="fileContainer" style="width: 200px" @change="onFileContainerChange">
          <el-option v-for="cn in fileContainers()" :key="cn" :label="cn" :value="cn" />
        </el-select>
        <el-button size="small" :disabled="filePath === '/'" @click="parentDir">返回上级</el-button>
        <span class="mono" style="font-size: 13px">{{ filePath }}</span>
        <!-- 上传：与下载分开授权；目标为当前目录，多选文件逐个提交（UploadFilled 形象图标） -->
        <el-button v-if="fileRow && verbAllowed('pods', 'upload', fileRow.metadata.namespace)" size="small" type="primary"
          :loading="uploading" @click="pickUpload"><el-icon v-if="!uploading"><UploadFilled /></el-icon>上传到当前目录</el-button>
        <input ref="fileUploadInput" type="file" multiple style="display: none" @change="onUploadPick" />
        <!-- 刷新：文字按钮带旋转图标，比孤立圆形图标按钮更显眼 -->
        <el-button size="small" :icon="Refresh" :loading="fileLoading" style="margin-left: auto" @click="loadDir">刷新</el-button>
      </div>
      <el-table :data="fileEntries" v-loading="fileLoading" border size="small" height="46vh"
        @selection-change="(s: any[]) => fileSel = s.filter((x) => !x.dir).map((x) => x.name)">
        <el-table-column type="selection" width="45" :selectable="(row: any) => !row.dir" />
        <el-table-column label="名称" min-width="300">
          <template #default="{ row }">
            <el-button v-if="row.dir" link type="primary" size="small" @click="enterDir(row.name)">{{ row.name }}/</el-button>
            <span v-else>{{ row.name }}</span>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="100">
          <template #default="{ row }">
            <el-tag :type="row.dir ? 'warning' : 'info'" size="small">{{ row.dir ? '目录' : '文件' }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
      <template #footer>
        <span class="dim" style="margin-right: 12px">已勾选 {{ fileSel.length }} 个文件{{ fileSel.length > 1 ? '（打包为 tar）' : '' }}</span>
        <el-button @click="fileDlg = false">关闭</el-button>
        <el-button v-if="fileRow && verbAllowed('pods', 'download', fileRow.metadata.namespace)"
          type="primary" :loading="downloading" :disabled="!fileSel.length" @click="downloadSelected"><el-icon v-if="!downloading"><Download /></el-icon>下载选中</el-button>
      </template>
    </el-dialog>

    <!-- 定时任务创建（定时重启 / 定时扩缩容）；关闭时恢复自动刷新 -->
    <ScheduleTaskDialog ref="scheduleDlg" @closed="scheduleDlgOpen = false" />
    <AiDialog ref="aiDlgRef" :title="aiTitle" />
    <!-- AI 修改结果 diff 确认：应用只是把草稿填入编辑器，真正生效仍需人工点「保存修改」 -->
    <el-dialog v-model="aiDiffDlg" title="AI 修改确认（diff）" width="760px" top="6vh" :close-on-click-modal="false">
      <el-alert type="warning" :closable="false" style="margin-bottom: 10px"
        title="请逐行核对改动（尤其密钥、镜像、注解等敏感字段）；「应用到编辑器」后仍需点「保存修改」才会生效。" />
      <div class="ai-diff">
        <div v-for="(l, i) in aiDiffLines" :key="i" :class="['ai-diff-line', 'ai-diff-' + l.type]">
          <span class="ai-diff-mark">{{ l.type === 'add' ? '+' : l.type === 'del' ? '-' : ' ' }}</span>{{ l.text }}
        </div>
      </div>
      <template #footer>
        <el-button @click="aiDiffDlg = false">放弃</el-button>
        <el-button type="primary" @click="applyAiEdit">应用到编辑器</el-button>
      </template>
    </el-dialog>

    <!-- 节点排空三步状态机：options（勾选项）→ review（排空计划二次确认，列出将被驱逐的 Pod）→ run（逐 Pod 驱电动效 + 中止） -->
    <el-dialog v-model="drainDlg" :title="`排空节点 - ${drainTargetName}`" width="680px" destroy-on-close
      :close-on-click-modal="drainStep !== 'run'" :show-close="drainStep !== 'run'" @closed="drainStep = 'options'">
      <!-- 第一步：选项 -->
      <template v-if="drainStep === 'options'">
        <el-alert type="warning" :closable="false" style="margin-bottom: 12px"
          title="排空会逐个驱逐该节点上的业务 Pod（调度到其他节点），驱逐请求尊重 PodDisruptionBudget。请确认集群剩余容量充足。" />
        <el-form label-width="130px">
          <el-form-item label="执行前封锁调度">
            <el-switch v-model="drainForm.cordon" />
            <span class="dim" style="margin-left: 8px">推荐开启：避免驱逐过程中新 Pod 调度上来</span>
          </el-form-item>
          <el-form-item label="驱逐 DS Pod">
            <el-checkbox v-model="drainForm.include_daemonsets">同时驱逐 DaemonSet Pod</el-checkbox>
            <span class="dim" style="margin-left: 8px">注意：DS Pod 通常容忍节点封锁污点，驱逐后可能马上在本节点重建——腾空节点时一般无需勾选</span>
          </el-form-item>
          <el-form-item label="删除裸 Pod">
            <el-checkbox v-model="drainForm.force_bare_pods">删除无控制器的裸 Pod</el-checkbox>
            <span class="dim" style="margin-left: 8px">裸 Pod 删除后不会自动重建，默认不碰</span>
          </el-form-item>
          <el-form-item label="优雅终止秒数">
            <el-input-number v-model="drainForm.grace_seconds" :min="0" :max="600" controls-position="right" style="width: 140px" />
            <span class="dim" style="margin-left: 8px">kubectl drain 默认 30</span>
          </el-form-item>
        </el-form>
      </template>
      <!-- 第二步：排空计划确认（二次确认核心：逐个列出将被驱逐的 Pod） -->
      <template v-else-if="drainStep === 'review'">
        <!-- 封锁后复核发现计划变化：新列表待重新确认（执行被拦回此步） -->
        <el-alert v-if="drainReviewNote" type="warning" :closable="false" style="margin-bottom: 10px"
          :title="drainReviewNote" />
        <el-alert type="warning" :closable="false" style="margin-bottom: 10px"
          :title="`即将驱逐以下 ${drainPlan.evictable.length} 个 Pod${drainForm.cordon ? '，并先封锁该节点调度' : ''}。驱逐逐个执行、尊重 PDB，请确认列表无误。`"
          :description="drainNodeUnschedulable ? '该节点当前已处于封锁状态（unschedulable=true）。' : ''" />
        <el-table :data="drainPlan.evictable" size="small" border max-height="260">
          <el-table-column type="index" label="#" width="46" />
          <el-table-column label="Pod" min-width="220" show-overflow-tooltip>
            <template #default="{ row }">{{ row.namespace }}/{{ row.name }}</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }"><el-tag size="small" effect="plain">{{ row.phase || '-' }}</el-tag></template>
          </el-table-column>
        </el-table>
        <div v-if="drainPlan.skipped.length" style="margin-top: 10px">
          <div class="dim" style="margin-bottom: 4px">跳过（不驱逐）</div>
          <el-tag v-for="s in drainPlan.skipped" :key="s.name" size="small" type="info" style="margin: 0 4px 4px 0">
            {{ s.name }}（{{ skipReasonLabel(s.reason) }}）
          </el-tag>
        </div>
        <el-alert v-if="!drainPlan.evictable.length" type="info" :closable="false" style="margin-top: 10px"
          title="该节点没有可驱逐的业务 Pod。" />
      </template>
      <!-- 第三步：执行（逐 Pod 驱电动效 + 中止） -->
      <template v-else>
        <div class="drain-run-head">
          <div class="drain-run-progress">
            <span class="drain-run-count">
              {{ drainDoneCount }}/{{ drainRunItems.length }}
              <template v-if="drainFailCount">　<span class="drain-run-fail">失败 {{ drainFailCount }}</span></template>
              <template v-if="drainCancelCount">　<span class="dim">未处理 {{ drainCancelCount }}</span></template>
            </span>
            <el-progress :percentage="drainRunItems.length ? Math.round(drainDoneCount / drainRunItems.length * 100) : 100"
              :status="drainRunFinished ? (drainFailCount ? 'warning' : 'success') : undefined" :stroke-width="10" style="flex: 1" />
          </div>
          <el-button v-if="!drainRunFinished" type="danger" plain size="small" :icon="SwitchButton" @click="drainAbort = true">中止</el-button>
        </div>
        <div v-if="drainAbort && !drainRunFinished" class="dim" style="margin-bottom: 8px">
          已请求中止：当前 Pod 处理完后停止，剩余 Pod 留在节点（{{ drainRunItems.length - drainDoneCount }} 个）。
        </div>
        <transition-group name="drain-card" tag="div" class="drain-cards">
          <div v-for="p in drainRunItems" :key="p.uid" class="drain-card" :class="`is-${p.state}`">
            <el-icon class="drain-card-icon">
              <Loading v-if="p.state === 'running'" />
              <CircleCheckFilled v-else-if="p.state === 'done'" />
              <CircleCloseFilled v-else-if="p.state === 'failed'" />
              <RemoveFilled v-else-if="p.state === 'cancelled'" />
              <Clock v-else />
            </el-icon>
            <div class="drain-card-meta">
              <div class="drain-card-name">{{ p.namespace }}/{{ p.name }}</div>
              <div class="drain-card-state">{{ drainCardStateLabel(p.state) }}</div>
            </div>
          </div>
        </transition-group>
        <el-alert v-if="drainRunFinished" :type="drainAbort ? 'warning' : 'info'" :closable="false" style="margin-top: 10px"
          :title="(drainAbort ? `排空已中止：${drainCancelCount} 个 Pod 未处理（留在节点）。` : '') + ((drainForm.cordon || drainNodeUnschedulable) ? '节点已封锁调度（unschedulable=true），处理完成后请在详情页「恢复调度」。' : '注意：未封锁调度，新 Pod 仍可能调度到该节点。')" />
      </template>
      <template #footer>
        <template v-if="drainStep === 'options'">
          <el-button @click="drainDlg = false">取消</el-button>
          <el-button type="primary" :loading="drainPlanning" @click="fetchDrainPlan">下一步：获取排空计划</el-button>
        </template>
        <template v-else-if="drainStep === 'review'">
          <el-button @click="drainStep = 'options'">返回调整</el-button>
          <el-button type="danger" :disabled="!drainPlan.evictable.length" @click="startDrainRun">确认并开始排空</el-button>
        </template>
        <el-button v-else type="primary" :disabled="!drainRunFinished" @click="drainDlg = false">
          {{ drainRunFinished ? '完成' : '排空中…' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search, UploadFilled, Download, MagicStick, Loading, CircleCheckFilled, CircleCloseFilled, RemoveFilled, SwitchButton, Clock } from '@element-plus/icons-vue'
import yaml from 'js-yaml'
import YamlEditor from '../components/YamlEditor.vue'
import LogViewer from '../components/LogViewer.vue'
import Terminal from '../components/Terminal.vue'
import ScheduleTaskDialog from '../components/ScheduleTaskDialog.vue'
import AiDialog from '../components/AiDialog.vue'
import SchedEditor, { schedFromK8s, schedToK8s, validateSched, schedModelDefault } from '../components/SchedEditor.vue'
import {
  listClusters, listNamespaces, listResources, getResource, updateResource,
  deleteResource, applyYAML, scaleResource, restartResource, resourceFormEdit, myGrants,
  stopResource, startResource, downloadPodFile, uploadPodFile, listPodFiles, describePod, getImageHistory,
  draftStage, draftList, draftDelete, nodeSummary, schedOptions, aiYamlExplain, aiYamlGenerate, aiYamlReview, aiYamlEdit,
  aiDiagnose, nodeCordon, nodeDrainPlan, nodeEvict,
} from '../api'
import { errMsg } from '../api/http'
import { ageOf, statusColor, podReady, podPhase, deployReady } from '../utils/format'
import { lineDiff } from '../utils/lineDiff'
import { splitReasoning } from '../utils/aimd'
import { useCRs } from '../composables/useCRs'

const route = useRoute() // 深链：总览页等跳转带 ?cluster=&ns=&resource= 定位

const classicResourceNav = [
  {
    group: '工作负载', items: [
      { label: 'Pod 列表', group: '', version: 'v1', resource: 'pods' },
      { label: '任务 (Job)', group: 'batch', version: 'v1', resource: 'jobs' },
      { label: '定时任务 (CronJob)', group: 'batch', version: 'v1', resource: 'cronjobs' },
      { label: '副本集 (ReplicaSet)', group: 'apps', version: 'v1', resource: 'replicasets' },
      { label: '应用列表 (Deployment)', group: 'apps', version: 'v1', resource: 'deployments' },
      { label: '有状态应用 (StatefulSet)', group: 'apps', version: 'v1', resource: 'statefulsets' },
      { label: '守护进程集 (DaemonSet)', group: 'apps', version: 'v1', resource: 'daemonsets' },
      { label: '自动扩缩容 (HPA)', group: 'autoscaling', version: 'v2', resource: 'horizontalpodautoscalers' },
      { label: '中断预算 (PDB)', group: 'policy', version: 'v1', resource: 'poddisruptionbudgets' },
    ],
  },
  {
    group: '网络', items: [
      { label: '服务 (Service)', group: '', version: 'v1', resource: 'services' },
      { label: '路由 (Ingress)', group: 'networking.k8s.io', version: 'v1', resource: 'ingresses' },
      { label: '端点 (Endpoints)', group: '', version: 'v1', resource: 'endpoints' },
      { label: '端点切片 (EndpointSlice)', group: 'discovery.k8s.io', version: 'v1', resource: 'endpointslices' },
      { label: '网络策略 (NetworkPolicy)', group: 'networking.k8s.io', version: 'v1', resource: 'networkpolicies' },
      { label: 'Ingress 类 (IngressClass)', group: 'networking.k8s.io', version: 'v1', resource: 'ingressclasses' },
    ],
  },
  {
    group: '配置', items: [
      { label: '配置 (ConfigMap)', group: '', version: 'v1', resource: 'configmaps' },
      { label: '密钥 (Secret)', group: '', version: 'v1', resource: 'secrets' },
      { label: '存储声明 (PVC)', group: '', version: 'v1', resource: 'persistentvolumeclaims' },
      { label: '资源配额 (ResourceQuota)', group: '', version: 'v1', resource: 'resourcequotas' },
      { label: '限制范围 (LimitRange)', group: '', version: 'v1', resource: 'limitranges' },
      { label: '服务账号 (ServiceAccount)', group: '', version: 'v1', resource: 'serviceaccounts' },
    ],
  },
  {
    group: '权限', items: [
      { label: '角色 (Role)', group: 'rbac.authorization.k8s.io', version: 'v1', resource: 'roles' },
      { label: '角色绑定 (RoleBinding)', group: 'rbac.authorization.k8s.io', version: 'v1', resource: 'rolebindings' },
      { label: '集群角色 (ClusterRole)', group: 'rbac.authorization.k8s.io', version: 'v1', resource: 'clusterroles' },
      { label: '集群角色绑定 (ClusterRoleBinding)', group: 'rbac.authorization.k8s.io', version: 'v1', resource: 'clusterrolebindings' },
    ],
  },
  {
    group: '集群', items: [
      { label: '节点 (Node)', group: '', version: 'v1', resource: 'nodes' },
      { label: '命名空间 (Namespace)', group: '', version: 'v1', resource: 'namespaces' },
      { label: '持久卷 (PV)', group: '', version: 'v1', resource: 'persistentvolumes' },
      { label: '存储类 (StorageClass)', group: 'storage.k8s.io', version: 'v1', resource: 'storageclasses' },
    ],
  },
]

const resourceNav = computed(() => classicResourceNav)

const clusters = ref<any[]>([])
const namespaces = ref<any[]>([])
const cluster = ref('')
const namespace = ref('')
const authStore = useAuthStore()
const resourceSelectionKey = `ky_resources_selection_${authStore.user?.id ?? authStore.user?.username ?? 'anonymous'}`
interface ResourceSelection {
  cluster: string
  namespaces: Record<string, string>
  resources: Record<string, string>
}
let canPersistResourceSelection = false
function loadResourceSelection(): ResourceSelection {
  try {
    const raw = localStorage.getItem(resourceSelectionKey)
    if (!raw) return { cluster: '', namespaces: {}, resources: {} }
    const value = JSON.parse(raw)
    return {
      cluster: typeof value?.cluster === 'string' ? value.cluster : '',
      namespaces: value?.namespaces && typeof value.namespaces === 'object' && !Array.isArray(value.namespaces)
        ? Object.fromEntries(Object.entries(value.namespaces).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))
        : {},
      resources: value?.resources && typeof value.resources === 'object' && !Array.isArray(value.resources)
        ? Object.fromEntries(Object.entries(value.resources).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))
        : {},
    }
  } catch {
    return { cluster: '', namespaces: {}, resources: {} }
  }
}
function saveResourceSelection(saveNamespace = false) {
  if (!cluster.value || !canPersistResourceSelection) return
  try {
    const saved = loadResourceSelection()
    saved.cluster = cluster.value
    saved.resources[cluster.value] = activeKey.value
    if (saveNamespace && isNamespaced.value) saved.namespaces[cluster.value] = namespace.value
    localStorage.setItem(resourceSelectionKey, JSON.stringify(saved))
  } catch { /* ignore storage unavailable */ }
}
function restoreResourceSelection(): boolean {
  const savedKey = loadResourceSelection().resources[cluster.value]
  if (!savedKey) return false
  const selected = filteredNav.value
    .flatMap((group) => group.items)
    .find((item) => `${item.group}|${item.version}|${item.resource}` === savedKey)
  if (!selected) return false
  cur.group = selected.group
  cur.version = selected.version
  cur.resource = selected.resource
  activeKey.value = savedKey
  if (!isNamespaced.value) namespace.value = ''
  return true
}
const search = ref('') // 搜索已由下方 watch(search) 300ms 防抖触发 load（服务端全量翻页过滤）
// 深链直达详情：总览「未就绪应用」跳转带 ?detail=应用名，首个列表批次到达后自动开详情抽屉；
// 消费一次即清空（后续切集群/换资源不再误触发）
const detailDeepLink = ref('')

// 点击复制（Pod/节点 IP 用）：clipboard API 不可用时降级 execCommand（非安全上下文的 http 部署）
function copyText(t?: string) {
  if (!t) return
  const done = () => ElMessage.success(`已复制 ${t}`)
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(t).then(done).catch(() => fallbackCopy(t, done))
  } else {
    fallbackCopy(t, done)
  }
}
function fallbackCopy(t: string, done: () => void) {
  const ta = document.createElement('textarea')
  ta.value = t
  document.body.appendChild(ta)
  ta.select()
  try { document.execCommand('copy'); done() } catch { ElMessage.warning('复制失败，请手动复制') }
  document.body.removeChild(ta)
}
const items = ref<any[]>([])
const truncated = ref(false) // 列表是否超过单次拉取上限（每命名空间 500 条）
const loading = ref(false)
const activeKey = ref('apps|v1|deployments')
const cur = reactive({ group: 'apps', version: 'v1', resource: 'deployments' })

function loadOpenedGroups(): string[] {
  try {
    const raw = localStorage.getItem('ky_res_nav_opened')
    if (raw) {
      const parsed: unknown = JSON.parse(raw)
      if (Array.isArray(parsed)) {
        return parsed
          .filter((group): group is string => typeof group === 'string')
          .map((group) => group === '项目' ? '工作负载' : group)
      }
    }
  } catch { /* ignore */ }
  return ['工作负载', '网络', '配置', '集群']
}
const resourceMenu = ref<{ open: (index: string) => void }>()
const openedGroups = ref<string[]>(loadOpenedGroups())
function onGroupOpen(index: string) {
  if (!openedGroups.value.includes(index)) openedGroups.value.push(index)
  saveOpenedGroups()
}
function onGroupClose(index: string) {
  openedGroups.value = openedGroups.value.filter((g) => g !== index)
  saveOpenedGroups()
}
function saveOpenedGroups() {
  try { localStorage.setItem('ky_res_nav_opened', JSON.stringify(openedGroups.value)) } catch { /* ignore */ }
}

const isNamespaced = computed(() => {
  // 内置集群级资源白名单（与 server/api/h_resource.go clusterScopedResources 保持同步）：
  // nav item 命中时优先读 crNamespaced（CR 走这条）；内置走硬编码兜底
  const clusterScoped = [
    'nodes', 'namespaces', 'clusterroles', 'clusterrolebindings',
    'customresourcedefinitions', 'persistentvolumes', 'storageclasses',
    'clusterissuers', 'ingressclasses', 'priorityclasses',
    'volumeattachments', 'apiservices', 'mutatingwebhookconfigurations',
    'validatingwebhookconfigurations', 'csidrivers', 'csinodes',
    'runtimeclasses', 'podsecuritypolicies', 'remoteclusters',
  ]
  if (clusterScoped.includes(cur.resource)) return false
  for (const g of filteredNav.value) for (const it of g.items) {
    if (it.resource === cur.resource && it.group === cur.group) {
      if (typeof (it as any).crNamespaced === 'boolean') return (it as any).crNamespaced
      return true
    }
  }
  return true
})
const currentLabel = computed(() => {
  for (const g of filteredNav.value) for (const it of g.items) {
    if (it.resource === cur.resource && it.group === cur.group) return it.label
  }
  return cur.resource
})

// ---------- 按授权过滤资源导航 ----------
const myPerms = ref<any[] | null>(null) // null = 未加载完成，暂不过滤

function listMatch(granted: string, want: string): boolean {
  if (!granted || granted === '*') return true
  return granted.split(',').map((s) => s.trim()).includes(want)
}

function resourceAllowed(resource: string): boolean {
  if (!myPerms.value) return true
  const rows = myPerms.value.filter((r) => r.cluster === '*' || r.cluster === cluster.value)
  if (!rows.length) return false
  // 与后端 ListableNamespaces 对齐：列表页需要 list 权限（get-only 授权不显示导航，避免点入 403）
  return rows.some((r) => listMatch(r.resources, resource) && listMatch(r.verbs, 'list'))
}

// 授权 ns 是否覆盖目标 ns（与后端 entryMatch 对齐：* / 空 = 全部；目标为空 = 集群级资源，ns 不参与判定）
function nsAllowed(granted: string, want: string): boolean {
  if (!granted || granted === '*') return true
  if (!want) return true
  return granted.split(',').map((s) => s.trim()).includes(want)
}

// 是否有某资源上的具体 verb（与后端 middleware.Allowed 对齐：资源+verb+命名空间 都需命中；
// ns 省略或空串 = 不限定（跨命名空间批量操作/集群级资源），与后端集群级判定一致）
function verbAllowed(resource: string, verb: string, ns?: string): boolean {
  if (!myPerms.value) return true
  const rows = myPerms.value.filter((r) => r.cluster === '*' || r.cluster === cluster.value)
  return rows.some((r) => listMatch(r.resources, resource) && listMatch(r.verbs, verb)
    && (!ns || nsAllowed(r.namespace, ns)))
}

// 是否有某资源上的动作（与后端 ActionAllowed 对齐：需 update 授权 + 动作命中 + 命名空间覆盖；
// actions 为空串时后端视为无任何动作（最严格），前端同样不放行——不再 || '*' 兜底）
function actionAllowed(resource: string, action: string, ns?: string): boolean {
  if (!myPerms.value) return true
  const rows = myPerms.value.filter((r) => r.cluster === '*' || r.cluster === cluster.value)
  return rows.some((r) => listMatch(r.resources, resource) && listMatch(r.verbs, 'update')
    && !!r.actions && listMatch(r.actions, action) && (!ns || nsAllowed(r.namespace, ns)))
}

// 表单模块是否可编辑：labels/annotations 属 metadata 层仅需 update 动词；其余模块与授权动作一一对应。
// taints 走 nodes 的 update（与 cordon 同口径，nodes 不进动作目录）；后端仍兜底拦截。
// 无权限时模块灰显只读（tab 标「只读」+ 内容 pointer-events 禁用 + 保存按钮隐藏；后端仍兜底拦截）
function sectionEditable(key: string): boolean {
  const ns = detailTarget.value?.metadata?.namespace
  if (key === 'labels' || key === 'annotations') return verbAllowed(authResKey.value, 'update', ns)
  if (key === 'taints') return authResKey.value === 'nodes' && verbAllowed('nodes', 'update')
  return actionAllowed(authResKey.value, key, ns)
}

// 是否有任意资源的 create 权限（YAML / 白屏部署入口；后端 apply 会逐对象再校验）
const canDeploy = computed(() => {
  if (!myPerms.value) return true
  const rows = myPerms.value.filter((r) => r.cluster === '*' || r.cluster === cluster.value)
  return rows.some((r) => listMatch(r.verbs, 'create'))
})

// 白屏部署可选的工作负载类型：按当前集群 create 权限过滤（后端 apply 逐对象校验兜底；
// ns 省略——命名空间在弹窗内才选，按资源级预过滤，ns 级差异留给后端拦截）
const fdKindDefs = [
  { value: 'deployment', label: '应用 (Deployment)', res: 'deployments' },
  { value: 'statefulset', label: '有状态应用 (StatefulSet)', res: 'statefulsets' },
  { value: 'daemonset', label: '守护进程集 (DaemonSet)', res: 'daemonsets' },
  { value: 'job', label: '任务 (Job)', res: 'jobs' },
  { value: 'cronjob', label: '定时任务 (CronJob)', res: 'cronjobs' },
]
const fdKindOptions = computed(() =>
  myPerms.value ? fdKindDefs.filter((k) => verbAllowed(k.res, 'create')) : fdKindDefs)

// 表单部署只面向工作负载页面；HPA/RBAC/网络策略等页面继续使用 YAML 部署，避免误导
const formDeployPages = new Set(['pods', 'deployments', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs'])
const canOpenFormDeploy = computed(() => formDeployPages.has(cur.resource) && fdKindOptions.value.length > 0)

const canCreateHpa = computed(() => cur.resource === 'horizontalpodautoscalers'
  && verbAllowed(authResKey.value, 'create', namespace.value || undefined))
const canCreateQuota = computed(() => cur.resource === 'resourcequotas'
  && verbAllowed(authResKey.value, 'create', namespace.value || undefined))

// 命名空间显示：有别名显示「别名 (原名)」
function nsLabel(ns: any): string {
  return ns?.alias ? `${ns.alias} (${ns.name})` : ns?.name || ''
}

// 按原名查别名（表格内使用；namespaces 列表里没有的命名空间直接显示原名）
function nsAliasLabel(name?: string): string {
  if (!name) return '-'
  const ns = namespaces.value.find((n) => n.name === name)
  return ns?.alias ? `${ns.alias} (${name})` : name
}

// ---------- 自定义资源发现 + 导航合并 ----------
const { load: loadCRs, get: getCRs } = useCRs()
const isCR = (resource: string) => {
  // 简化判断：CR 的 nav item 上挂了 crKind；其他都是内置
  for (const g of filteredNav.value) for (const it of g.items) {
    if (it.resource === resource && (it as any).crKind) return true
  }
  return false
}

// 鉴权用资源键：与后端 resKeyForAuth 对齐——CR 用 group/plural 全限定名，内置资源用裸 plural。
// （此前 CR 行的操作按钮直接用裸 plural 匹配授权，永远命中不了 group/plural 形式的授权，
//   导致有删除/更新权限的用户也看不到按钮）
const authResKey = computed(() => (isCR(cur.resource) ? `${cur.group}/${cur.resource}` : cur.resource))

// 「自定义资源」组：按当前集群 + 授权过滤；空集返回 null（不显示组）
const crsGroup = computed(() => {
  const items = getCRs(cluster.value) || []
  const visible = myPerms.value
    ? items.filter(c => resourceAllowed(`${c.group}/${c.plural}`))
    : items
  if (!visible.length) return null
  return {
    group: '自定义资源',
    items: visible.sort((a, b) => a.kind.localeCompare(b.kind)).map(c => ({
      label: c.kind, group: c.group, version: c.version, resource: c.plural,
      crKind: c.kind, crGroup: c.group, crNamespaced: c.namespaced,
    })),
  }
})

const filteredNav = computed(() => {
  if (!myPerms.value) return resourceNav.value
  const base = resourceNav.value
    .map((g) => ({ ...g, items: g.items.filter((it) => resourceAllowed(it.resource)) }))
    .filter((g) => g.items.length > 0)
  const crg = crsGroup.value
  if (crg) base.push(crg)
  return base
})

// 深链或权限变化切换当前资源时，仅展开所选资源所在组；手动收起仍按用户偏好记忆
watch(activeKey, (key) => {
  const selectedGroup = filteredNav.value.find((group) =>
    group.items.some((item) => `${item.group}|${item.version}|${item.resource}` === key))
  if (selectedGroup && !openedGroups.value.includes(selectedGroup.group)) {
    openedGroups.value = [...openedGroups.value, selectedGroup.group]
    saveOpenedGroups()
    nextTick(() => resourceMenu.value?.open(selectedGroup.group))
  }
}, { deep: true })

// 当前选中资源在（新）集群无权限时切到第一个可见资源
function ensureSelection() {
  if (!myPerms.value || !filteredNav.value.length) return
  const visible = filteredNav.value.some((g) => g.items.some((it) => it.resource === cur.resource && it.group === cur.group))
  if (!visible) {
    const first = filteredNav.value[0].items[0]
    cur.group = first.group
    cur.version = first.version
    cur.resource = first.resource
    activeKey.value = `${first.group}|${first.version}|${first.resource}`
  }
}

// 深链定位：?cluster=&ns=&resource=&search=&detail=（总览页异常 Pod / 未就绪应用等点击跳转）。
// 集群在 loadClusters 里优先选取；这里在导航就绪后定位命名空间与资源，无权限的项静默忽略
function applyRouteQuery() {
  const q = route.query
  if (typeof q.ns === 'string' && q.ns && namespaces.value.some((n: any) => n.name === q.ns)) {
    namespace.value = q.ns
  }
  // 深链搜索词（总览跳转带筛选词）：填进搜索栏（用户可见可改），watch(search) 防抖触发 load
  if (typeof q.search === 'string' && q.search) {
    search.value = q.search
  }
  // 深链直达详情（总览未就绪应用跳转）：首个列表批次加载完成后打开对应资源的详情抽屉
  if (typeof q.detail === 'string' && q.detail) {
    detailDeepLink.value = q.detail
  }
  if (typeof q.resource === 'string' && q.resource) {
    for (const g of filteredNav.value) {
      for (const it of g.items) {
        if (it.resource === q.resource) {
          cur.group = it.group
          cur.version = it.version
          cur.resource = it.resource
          activeKey.value = `${it.group}|${it.version}|${it.resource}`
          if (!isNamespaced.value) namespace.value = '' // 集群级资源不带 ns，与手动点导航一致
          return
        }
      }
    }
  }
}

// 详情
const detailDlg = ref(false)
const detailYaml = ref('')
const detailTitle = ref('')
const detailTarget = ref<any>(null)
const detailObj = ref<any>(null)
const saving = ref(false)

// 表单编辑（白屏）
const detailTab = ref('form')
const formTab = ref('image')
const formSaving = ref(false)
const formModel = ref<any[]>([]) // 工作负载容器表单
const svcForm = reactive({ ports: [] as any[], selector: [] as any[] })
// Ingress 路由行编辑（routes 模块）：与白屏创建同构，port 允许数字或端口名
const ingForm = reactive({ rows: [] as Array<{ host: string; path: string; pathType: string; svc: string; port: any }> })
const metaForm = reactive<{ labels: any[]; annotations: any[] }>({ labels: [], annotations: [] }) // 标签/注解（metadata 层）
// ConfigMap / Secret 数据编辑（data 顶层字段，全量覆盖；Secret 的值展示时 base64 解码、保存时重编码，
// 解码失败的二进制内容只读原样透传——保存不会破坏）
const dataForm = reactive({ rows: [] as Array<{ key: string; value: string; binary?: boolean }> })
// PVC 容量扩容（存储类/访问模式创建后不可改，仅展示）
const pvcCapForm = reactive({ cur: '', size: '', storageClass: '', accessMode: '' })
// 调度策略编辑（scheduling 模块）：Pod 级（非容器级），SchedEditor 的 UI 行模型
const schedForm = ref(schedModelDefault())
// 读回时检测到表单不支持的既有亲和配置 → 顶部黄色警示（保存后端也会拒绝）
const schedUnmodeledFlag = ref(false)
// 节点污点编辑（taints 模块）：spec.taints 全量覆盖
const taintForm = reactive({ rows: [] as Array<{ key: string; value: string; effect: string }> })
// 系统管理型污点（与后端 systemTaintKeys 同表）：kubelet/控制器维护，只能原样保留（行只读，删改后端拒绝）
const TAINT_SYSTEM_KEYS = ['node.kubernetes.io/not-ready', 'node.kubernetes.io/unreachable', 'node.kubernetes.io/unschedulable', 'node.cloudprovider.kubernetes.io/uninitialized', 'node.kubernetes.io/network-unavailable']
const isSystemTaint = (key: string) => TAINT_SYSTEM_KEYS.includes((key || '').trim())
// 当前表单中的系统污点行（警示提示用）
const taintSystemRows = computed(() => taintForm.rows.filter((r) => isSystemTaint(r.key)).map((r) => r.key.trim()))
// 节点候选（调度策略 nodeName 下拉：[{name, unschedulable}]；无调度候选权限时 SchedEditor 手输兜底）
const nodeOptions = ref<{ name: string; unschedulable?: boolean }[]>([])
// 节点标签聚合（调度策略下拉候选）：labelKeys = 全部标签键去重排序；labelValues = 键 → 实际值集合
const nodeLabelKeys = ref<string[]>([])
const nodeLabelValues = ref<Record<string, string[]>>({})
// 节点污点聚合（污点编辑行下拉候选，系统管理型已由后端排除）：taintKeys + 键 → 值集合
const taintKeys = ref<string[]>([])
const taintValues = ref<Record<string, string[]>>({})
function taintValuesOf(key: string): string[] {
  return (taintValues.value || {})[key] || []
}

// 「键 = 值」整对候选（污点值可能为空：空值对显示为 "key="）
const taintPairs = computed(() => {
  const out: string[] = []
  for (const k of taintKeys.value) {
    const vs = taintValues.value[k] || []
    if (vs.length) for (const v of vs) out.push(`${k}=${v}`)
    else out.push(`${k}=`)
  }
  return out
})
// 行的「键 = 值」视图：选中/手输均按第一个 = 拆回 row.key/row.value（无 = 整串作键、值空）
function taintKvOf(row: { key: string; value: string }): string {
  return row.key ? `${row.key}=${row.value || ''}` : ''
}
function setTaintKv(row: { key: string; value: string }, pair: string) {
  if (pair == null) { row.key = ''; row.value = ''; return }
  const i = pair.indexOf('=')
  if (i >= 0) {
    row.key = pair.slice(0, i).trim()
    row.value = pair.slice(i + 1).trim()
  } else {
    row.key = pair.trim()
    row.value = ''
  }
}

// 存储挂载模块：pod 级卷解析结果（容器间共享）；可建模的 pvc/configmap/secret/emptyDir 进表单，
// 其余（hostPath/projected 等）原样保留；initContainers 的挂载不进表单、引用的卷不可丢
const volState = reactive<{ raw: any[]; modeled: Record<string, { type: string; refName: string }>; initNames: Set<string> }>({
  raw: [], modeled: {}, initNames: new Set(),
})
const refOptions = ref<Record<string, string[]>>({}) // 引用对象下拉缓存：`${ns}/${type}` 按命名空间懒加载（编辑抽屉与部署表单的 ns 可能不同，不能只按 type 缓存）

const editNS = () => detailTarget.value?.metadata?.namespace || ''

async function loadRefOptions(type: string, ns: string) {
  const resMap: Record<string, string> = { pvc: 'persistentvolumeclaims', configmap: 'configmaps', secret: 'secrets' }
  const res = resMap[type]
  if (!res || !cluster.value || !ns) return
  const k = `${ns}/${type}`
  if (refOptions.value[k]) return
  try {
    const r = await listResources(cluster.value, {
      group: '', version: 'v1', resource: res,
      namespace: ns,
      _silent: 1,
    })
    refOptions.value[k] = (r.data.items || []).map((i: any) => i.metadata.name)
  } catch { refOptions.value[k] = [] }
}

function refOptsOf(type: string, ns: string): string[] {
  return refOptions.value[`${ns}/${type}`] || []
}

// 环境变量引用键：选中 Secret/ConfigMap 后懒加载其 data keys（需对应资源的 get 权限；
// 无权限或对象不存在时下拉为空，allow-create 仍可手输，不阻塞编辑）
const envKeyOptions = ref<Record<string, string[]>>({}) // `${ns}/${type}/${name}` → data keys

async function loadEnvKeys(type: string, ns: string, name: string) {
  if ((type !== 'secret' && type !== 'configmap') || !cluster.value || !ns) return
  const n = (name || '').trim()
  if (!n) return
  const k = `${ns}/${type}/${n}`
  if (envKeyOptions.value[k]) return
  try {
    const r = await getResource(cluster.value, {
      group: '', version: 'v1',
      resource: type === 'secret' ? 'secrets' : 'configmaps',
      namespace: ns, name: n, _silent: 1,
    })
    envKeyOptions.value[k] = Object.keys(r.data?.data || {})
  } catch { envKeyOptions.value[k] = [] }
}

const envKeyOpts = (type: string, ns: string, name: string): string[] => {
  const n = (name || '').trim()
  return n ? (envKeyOptions.value[`${ns}/${type}/${n}`] || []) : []
}

// 新建 PVC 并挂载：存储类/容量/访问模式是 PVC 创建时的属性（挂载已有 PVC 不涉及存储类），
// 创建成功自动回填到当前挂载行；走 apply 通道，需 persistentvolumeclaims 的 create 权限。
// pvcStandalone = 列表页「白屏创建」（无挂载行回填，创建后刷新列表）
const pvcDlg = ref(false)
const pvcCreating = ref(false)
const pvcForm = reactive({ name: '', storageClass: '', size: '10Gi', accessMode: 'ReadWriteOnce' })
const pvcStandalone = ref(false)
let pvcTargetRow: any = null

function openNewPvc(row: any) {
  pvcTargetRow = row
  pvcStandalone.value = false
  // 默认名：工作负载名-data（截断并清理非法字符）
  const base = (detailTarget.value?.metadata?.name || 'data').toLowerCase().replace(/[^a-z0-9-]/g, '').slice(0, 40) || 'data'
  pvcForm.name = `${base}-data`
  pvcForm.storageClass = ''
  pvcForm.size = '10Gi'
  pvcForm.accessMode = 'ReadWriteOnce'
  pvcDlg.value = true
  loadStorageClasses()
}

async function createPvc() {
  const name = pvcForm.name.trim()
  const size = pvcForm.size.trim()
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)) { ElMessage.warning('PVC 名需为小写字母/数字/中划线'); return }
  if (!/^\d+(\.\d+)?(Ki|Mi|Gi|Ti)?$/.test(size)) { ElMessage.warning('容量格式如 10Gi / 500Mi'); return }
  const ns = pvcStandalone.value ? createNS.value : (detailTarget.value?.metadata?.namespace || '')
  if (!ns) { ElMessage.warning('请选择命名空间'); return }
  const spec: any = { accessModes: [pvcForm.accessMode], resources: { requests: { storage: size } } }
  if (pvcForm.storageClass) spec.storageClassName = pvcForm.storageClass
  const pvc = { apiVersion: 'v1', kind: 'PersistentVolumeClaim', metadata: { name, namespace: ns }, spec }
  pvcCreating.value = true
  try {
    const res = await applyYAML(cluster.value, { yaml: yaml.dump(pvc), dry_run: false, namespace: ns })
    const r = (res.data.results || [])[0]
    if (!res.data.ok || !r?.ok) { ElMessage.error(`PVC 创建失败：${r?.message || '未知错误'}`); return }
    ElMessage.success(`PVC ${name} 已创建`)
    refOptions.value[`${ns}/pvc`] = [...(refOptions.value[`${ns}/pvc`] || []), name] // 新建的下拉立即可选
    if (pvcTargetRow) { pvcTargetRow.type = 'pvc'; pvcTargetRow.refName = name; pvcTargetRow.volName = '' }
    pvcDlg.value = false
    if (pvcStandalone.value) load() // 独立创建：刷新列表
  } catch (e) { ElMessage.error(errMsg(e)) } finally { pvcCreating.value = false }
}

// 新建 ConfigMap / Secret 并挂载：挂载行引用对象不存在时可就地创建（key-value 白屏编辑）；
// Secret 用 stringData 提交（apiserver 自动编码为 data）；走 apply 通道，需对应资源的 create 权限。
// cfgStandalone = 列表页「白屏创建」
const cfgDlg = ref(false)
const cfgCreating = ref(false)
const cfgForm = reactive({ kind: 'configmap' as 'configmap' | 'secret', name: '', entries: [] as Array<{ key: string; value: string }> })
const cfgStandalone = ref(false)
// 创建弹窗的命名空间：独立创建时必选；挂载行创建时固定为工作负载所在命名空间
const createNS = ref('')
let cfgTargetRow: any = null

function openNewCfg(row: any, kind: 'configmap' | 'secret') {
  cfgTargetRow = row
  cfgForm.kind = kind
  cfgStandalone.value = false
  cfgForm.name = ''
  cfgForm.entries = [{ key: '', value: '' }]
  cfgDlg.value = true
}

async function createCfg() {
  const name = cfgForm.name.trim()
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)) { ElMessage.warning('名称需为小写字母/数字/中划线'); return }
  const entries = cfgForm.entries.filter((e) => e.key.trim())
  if (!entries.length) { ElMessage.warning('至少填写一条数据（键不能为空）'); return }
  const data: Record<string, string> = {}
  for (const e of entries) {
    const k = e.key.trim()
    if (!/^[-._a-zA-Z0-9]+$/.test(k)) { ElMessage.warning(`数据键「${k}」不合法（字母/数字/中划线/点/下划线）`); return }
    if (data[k] !== undefined) { ElMessage.warning(`数据键「${k}」重复`); return }
    data[k] = e.value
  }
  const ns = cfgStandalone.value ? createNS.value : (detailTarget.value?.metadata?.namespace || '')
  if (!ns) { ElMessage.warning('请选择命名空间'); return }
  const obj: any = cfgForm.kind === 'configmap'
    ? { apiVersion: 'v1', kind: 'ConfigMap', metadata: { name, namespace: ns }, data }
    : { apiVersion: 'v1', kind: 'Secret', metadata: { name, namespace: ns }, type: 'Opaque', stringData: data }
  cfgCreating.value = true
  try {
    const res = await applyYAML(cluster.value, { yaml: yaml.dump(obj), dry_run: false, namespace: ns })
    const r = (res.data.results || [])[0]
    if (!res.data.ok || !r?.ok) { ElMessage.error(`创建失败：${r?.message || '未知错误'}`); return }
    ElMessage.success(`${cfgForm.kind === 'configmap' ? 'ConfigMap' : 'Secret'} ${name} 已创建`)
    refOptions.value[`${ns}/${cfgForm.kind}`] = [...(refOptions.value[`${ns}/${cfgForm.kind}`] || []), name] // 新建的下拉立即可选
    if (cfgTargetRow) { cfgTargetRow.type = cfgForm.kind; cfgTargetRow.refName = name; cfgTargetRow.volName = '' }
    cfgDlg.value = false
    if (cfgStandalone.value) load() // 独立创建：刷新列表
  } catch (e) { ElMessage.error(errMsg(e)) } finally { cfgCreating.value = false }
}

// ---------- 列表页白屏创建（ConfigMap / Secret / PVC / Service / Ingress）----------
const simpleCreateRes = ['configmaps', 'secrets', 'persistentvolumeclaims', 'services', 'ingresses']
const canCreateCur = computed(() => simpleCreateRes.includes(cur.resource)
  && verbAllowed(authResKey.value, 'create', namespace.value || undefined))

function openCreateCur() {
  createNS.value = namespace.value || '' // 默认带当前命名空间过滤；未选则必填
  if (cur.resource === 'persistentvolumeclaims') {
    pvcStandalone.value = true
    pvcTargetRow = null
    pvcForm.name = ''
    pvcForm.storageClass = ''
    pvcForm.size = '10Gi'
    pvcForm.accessMode = 'ReadWriteOnce'
    pvcDlg.value = true
    loadStorageClasses()
    return
  }
  if (cur.resource === 'services') {
    openCreateSvc()
    return
  }
  if (cur.resource === 'ingresses') {
    openCreateIng()
    return
  }
  cfgStandalone.value = true
  cfgTargetRow = null
  cfgForm.kind = cur.resource === 'secrets' ? 'secret' : 'configmap'
  cfgForm.name = ''
  cfgForm.entries = [{ key: '', value: '' }]
  cfgDlg.value = true
}

// ---------- Service 白屏创建 ----------
const svcCreateDlg = ref(false)
const svcCreating = ref(false)
const svcCreateForm = reactive({
  name: '', type: 'ClusterIP',
  selector: [] as Array<{ key: string; value: string }>,
  ports: [] as Array<{ name: string; port: any; targetPort: string; protocol: string }>,
})

function openCreateSvc() {
  svcCreateForm.name = ''
  svcCreateForm.type = 'ClusterIP'
  svcCreateForm.selector = [{ key: 'app', value: '' }]
  svcCreateForm.ports = [{ name: '', port: 80, targetPort: '', protocol: 'TCP' }]
  svcCreateDlg.value = true
}

async function createSvc() {
  const name = svcCreateForm.name.trim()
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)) { ElMessage.warning('Service 名需为小写字母/数字/中划线'); return }
  if (!createNS.value) { ElMessage.warning('请选择命名空间'); return }
  const selector: Record<string, string> = {}
  for (const s of svcCreateForm.selector) {
    if (!s.key.trim()) continue
    if (!/^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?(\/[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$/.test(s.key.trim())) {
      ElMessage.warning(`选择器键「${s.key}」不是合法的标签键`); return
    }
    if (selector[s.key.trim()] !== undefined) { ElMessage.warning(`选择器键「${s.key}」重复`); return }
    selector[s.key.trim()] = s.value
  }
  if (!Object.keys(selector).length) { ElMessage.warning('至少填写一个选择器标签，否则 Service 不会关联任何 Pod'); return }
  const names = new Set<string>()
  const seen = new Set<string>()
  const ports: any[] = []
  for (const p of svcCreateForm.ports) {
    if (p.port === null || p.port === undefined || p.port === '') continue
    if (p.name) {
      if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(p.name) || p.name.length > 15) {
        ElMessage.warning(`端口名「${p.name}」需为小写字母/数字/中划线且不超过 15 字符`); return
      }
      if (names.has(p.name)) { ElMessage.warning(`端口名「${p.name}」重复`); return }
      names.add(p.name)
    }
    const key = `${p.port}/${p.protocol || 'TCP'}`
    if (seen.has(key)) { ElMessage.warning(`端口 ${p.port}（${p.protocol || 'TCP'}）重复`); return }
    seen.add(key)
    ports.push({ name: p.name || undefined, port: p.port, targetPort: portVal(p.targetPort) ?? p.port, protocol: p.protocol || 'TCP' })
  }
  if (!ports.length) { ElMessage.warning('至少填写一个端口'); return }
  const spec: any = { selector, ports }
  if (svcCreateForm.type !== 'ClusterIP') spec.type = svcCreateForm.type
  const obj = { apiVersion: 'v1', kind: 'Service', metadata: { name, namespace: createNS.value }, spec }
  svcCreating.value = true
  try {
    const res = await applyYAML(cluster.value, { yaml: yaml.dump(obj), dry_run: false, namespace: createNS.value })
    const r = (res.data.results || [])[0]
    if (!res.data.ok || !r?.ok) { ElMessage.error(`创建失败：${r?.message || '未知错误'}`); return }
    ElMessage.success(`Service ${name} 已创建`)
    svcCreateDlg.value = false
    load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { svcCreating.value = false }
}

// ---------- Ingress 白屏创建 ----------
// 行式路由（域名/路径/后端服务/端口），保存时按域名自动归组为 rules；后端服务下拉来自所选命名空间
const ingCreateDlg = ref(false)
const ingCreating = ref(false)
const ingCreateForm = reactive({ name: '', ingressClass: '', rows: [] as Array<{ host: string; path: string; pathType: string; svc: string; port: any }> })
const svcNameOptions = ref<string[]>([])

async function loadSvcNames() {
  if (!cluster.value || !createNS.value) { svcNameOptions.value = []; return }
  try {
    const r = await listResources(cluster.value, { group: '', version: 'v1', resource: 'services', namespace: createNS.value, _silent: 1 })
    svcNameOptions.value = (r.data.items || []).map((i: any) => i.metadata.name)
  } catch { svcNameOptions.value = [] }
}

function openCreateIng() {
  ingCreateForm.name = ''
  ingCreateForm.ingressClass = ''
  ingCreateForm.rows = [{ host: '', path: '/', pathType: 'Prefix', svc: '', port: 80 }]
  svcNameOptions.value = []
  ingCreateDlg.value = true
  loadSvcNames()
}

async function createIng() {
  const name = ingCreateForm.name.trim()
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)) { ElMessage.warning('Ingress 名需为小写字母/数字/中划线'); return }
  if (!createNS.value) { ElMessage.warning('请选择命名空间'); return }
  const rules: any[] = []
  const seen = new Set<string>()
  for (const r of ingCreateForm.rows) {
    const host = r.host.trim()
    const svc = r.svc.trim()
    if (!host && !svc) continue // 整行空白跳过
    if (!host || !svc) { ElMessage.warning('每条路由需同时填写域名与后端服务'); return }
    if (!/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9.]*[a-z0-9])?)+$/.test(host)) {
      ElMessage.warning(`域名「${host}」格式不正确（如 app.example.com）`); return
    }
    const port = Number(r.port)
    if (!port || port < 1 || port > 65535) { ElMessage.warning(`路由 ${host} 的后端端口需为 1-65535`); return }
    const path = r.path.trim() || '/'
    const key = `${host}${path}`
    if (seen.has(key)) { ElMessage.warning(`路由 ${key} 重复`); return }
    seen.add(key)
    let rule = rules.find((x: any) => x.host === host)
    if (!rule) { rule = { host, http: { paths: [] } }; rules.push(rule) }
    // v1 结构：rules[].http.paths[]；直接挂 rule.paths 会被 apiserver 字段校验静默丢弃（host 下无路由）
    rule.http.paths.push({ path, pathType: r.pathType || 'Prefix', backend: { service: { name: svc, port: { number: port } } } })
  }
  if (!rules.length) { ElMessage.warning('至少填写一条路由规则'); return }
  const spec: any = { rules }
  if (ingCreateForm.ingressClass.trim()) spec.ingressClassName = ingCreateForm.ingressClass.trim()
  const obj = { apiVersion: 'networking.k8s.io/v1', kind: 'Ingress', metadata: { name, namespace: createNS.value }, spec }
  ingCreating.value = true
  try {
    const res = await applyYAML(cluster.value, { yaml: yaml.dump(obj), dry_run: false, namespace: createNS.value })
    const r = (res.data.results || [])[0]
    if (!res.data.ok || !r?.ok) { ElMessage.error(`创建失败：${r?.message || '未知错误'}`); return }
    ElMessage.success(`Ingress ${name} 已创建`)
    ingCreateDlg.value = false
    load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { ingCreating.value = false }
}

// 卷名分配：解析行沿用原卷名（引用名可改，卷名不变 = init 容器引用不断）；新行按 (type, refName)
// 分组共享一个卷（同 PVC 挂多个路径），emptyDir 每行独立卷；返回卷描述供保存
function storageAssignVolNames(): { err: string | null; descs: any[] } {
  const used = new Set<string>(volState.raw.map((v: any) => v.name))
  const descs: any[] = []
  const uniq = (base: string) => {
    let n = base, i = 2
    while (used.has(n)) n = `${base}-${i++}`
    return n
  }
  for (const ct of formModel.value) {
    for (const m of ct.mounts) {
      const ref = String(m.refName || '').trim()
      if (m.volName) {
        const d = descs.find((x: any) => x.name === m.volName)
        if (d) {
          if (d.type !== m.type || d.refName !== ref) return { err: `卷 ${m.volName} 被不同引用的挂载共用，请保持引用对象一致`, descs }
        } else {
          descs.push({ name: m.volName, type: m.type, refName: ref })
        }
        continue
      }
      if (m.type === 'emptydir') {
        const name = uniq('emptydir')
        used.add(name)
        descs.push({ name, type: 'emptydir', refName: '' })
        m.volName = name
        continue
      }
      let d = descs.find((x: any) => x.type !== 'emptydir' && x.type === m.type && x.refName === ref)
      if (!d) {
        const name = uniq(ref)
        used.add(name)
        d = { name, type: m.type, refName: ref }
        descs.push(d)
      }
      m.volName = d.name
    }
  }
  return { err: null, descs }
}

const formSections = computed(() => {
  if (['deployments', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs'].includes(cur.resource)) {
    return [
      { key: 'image', label: '镜像' },
      { key: 'command', label: '入口 / 命令' },
      { key: 'ports', label: '端口' },
      { key: 'resources', label: '资源配置' },
      { key: 'env', label: '环境变量' },
      { key: 'probes', label: '健康检查' },
      { key: 'storage', label: '存储挂载' },
      { key: 'scheduling', label: '调度策略' },
      { key: 'labels', label: '标签' },
      { key: 'annotations', label: '注解' },
    ]
  }
  if (cur.resource === 'nodes') {
    return [
      { key: 'taints', label: '污点' },
      { key: 'labels', label: '标签' },
      { key: 'annotations', label: '注解' },
    ]
  }
  if (cur.resource === 'services') {
    return [
      { key: 'ports', label: '端口' },
      { key: 'selector', label: '选择器' },
      { key: 'labels', label: '标签' },
      { key: 'annotations', label: '注解' },
    ]
  }
  if (cur.resource === 'ingresses') {
    return [
      { key: 'routes', label: '路由' },
      { key: 'labels', label: '标签' },
      { key: 'annotations', label: '注解' },
    ]
  }
  if (cur.resource === 'configmaps' || cur.resource === 'secrets') {
    return [
      { key: 'data', label: '数据' },
      { key: 'labels', label: '标签' },
      { key: 'annotations', label: '注解' },
    ]
  }
  if (cur.resource === 'persistentvolumeclaims') {
    return [
      { key: 'storage', label: '容量' },
      { key: 'labels', label: '标签' },
      { key: 'annotations', label: '注解' },
    ]
  }
  return []
})
const currentSectionLabel = computed(() => formSections.value.find((s) => s.key === formTab.value)?.label || '')

// ---------- 镜像历史（Deployment/StatefulSet/DaemonSet；数据来自 K8s 原生版本记录）----------
const imgHistSupported = computed(() => ['deployments', 'statefulsets', 'daemonsets'].includes(cur.resource))
const imgHistDlg = ref(false)
const imgHistLoading = ref(false)
const imgHistItems = ref<any[]>([])

async function openImageHistory() {
  const row = detailTarget.value
  if (!row || !cluster.value) return
  imgHistDlg.value = true
  imgHistLoading.value = true
  try {
    const res = await getImageHistory(cluster.value, {
      group: cur.group, version: cur.version, resource: cur.resource,
      namespace: row.metadata.namespace || '', name: row.metadata.name,
    })
    imgHistItems.value = res.data.items || []
    if (!imgHistItems.value.length) ElMessage.info('没有历史版本记录（资源创建后未变更过模板，或历史已被 revisionHistoryLimit 清理）')
  } catch (e: any) {
    ElMessage.error(errMsg(e))
    imgHistItems.value = []
  } finally { imgHistLoading.value = false }
}

// 回填历史镜像：按容器名匹配替换（init 容器不在表单编辑范围内，仅展示）；保存仍走「镜像」页保存按钮
function applyImageHistory(item: any) {
  let hit = 0
  for (const h of item.containers || []) {
    const ct = formModel.value.find((x: any) => x.name === h.name)
    if (ct && h.image) { ct.image = h.image; hit++ }
  }
  if (!hit) { ElMessage.warning('该历史版本与当前模板没有同名容器'); return }
  imgHistDlg.value = false
  ElMessage.success(`已回填 ${hit} 个容器的镜像，请在「镜像」页确认后保存`)
}

// ---------- 运行实例（应用详情内查看 Pod，Rancher 风格）----------
const hasPodsView = computed(() => ['deployments', 'statefulsets', 'daemonsets'].includes(cur.resource))
const appPods = ref<any[]>([])
const appPodsLoading = ref(false)
const appPodsErr = ref('')

function onDetailTab(name: any) {
  if (name === 'pods') loadAppPods()
  if (name === 'overview') loadOverview()
}

function podRestarts(pod: any): number {
  return (pod.status?.containerStatuses || []).reduce((s: number, c: any) => s + (c.restartCount || 0), 0)
}

// Pod 是否匹配工作负载 selector（matchLabels + matchExpressions 全集语义）。
// matchLabels 已作为 label_selector 下推服务端过滤，这里兜底校验并补 matchExpressions
function podMatchSelector(p: any, selector: any): boolean {
  for (const [k, v] of Object.entries(selector?.matchLabels || {})) {
    if (p.metadata?.labels?.[k] !== v) return false
  }
  for (const e of selector?.matchExpressions || []) {
    const val = p.metadata?.labels?.[e.key]
    const vals: string[] = e.values || []
    switch (e.operator) {
      case 'In': if (!vals.includes(val)) return false; break
      case 'NotIn': if (vals.includes(val)) return false; break
      case 'Exists': if (val === undefined) return false; break
      case 'DoesNotExist': if (val !== undefined) return false; break
      case 'Gt': if (val === undefined || !(Number(val) > Number(vals[0]))) return false; break
      case 'Lt': if (val === undefined || !(Number(val) < Number(vals[0]))) return false; break
      default: return false
    }
  }
  return true
}

async function loadAppPods() {
  const row = detailTarget.value
  if (!row || !cluster.value) return
  const selector = detailObj.value?.spec?.selector || {}
  const matchLabels = Object.entries(selector.matchLabels || {})
  appPodsLoading.value = true
  appPodsErr.value = ''
  try {
    // 标签选择器下推 K8s 服务端过滤：此前全量拉回前端再筛，命名空间 Pod 超过列表接口
    // 单页上限 500 时，目标应用的 Pod 可能一页都进不来（表现为运行实例为空）
    const res = await listResources(cluster.value, {
      group: '', version: 'v1', resource: 'pods',
      namespace: row.metadata.namespace || '',
      ...(matchLabels.length ? { label_selector: matchLabels.map(([k, v]) => `${k}=${v}`).join(',') } : {}),
      _silent: 1, // 失败已在抽屉内 err 展示，不弹全局提示
    })
    const pods = res.data.items || []
    appPods.value = pods.filter((p: any) => podMatchSelector(p, selector))
  } catch (e: any) {
    appPodsErr.value = errMsg(e)
    appPods.value = []
  } finally { appPodsLoading.value = false }
}

// 部署
const deployDlg = ref(false)
const deployYaml = ref('')
const deployNS = ref('')
const deploying = ref(false)
const deployResults = ref<any[]>([])

// ---------- AI 助手（只读：解释当前 YAML / 按描述生成草稿） ----------
const aiDlgRef = ref<InstanceType<typeof AiDialog>>()
const aiTitle = ref('AI 解释')
const aiGenPrompt = ref('')
const aiGenerating = ref(false)

function explainYaml() {
  if (!detailYaml.value.trim()) { ElMessage.warning('当前没有 YAML 内容可解释'); return }
  aiTitle.value = `AI 解释 — ${cur.resource}/${detailTarget.value?.metadata?.name || ''}`
  aiDlgRef.value?.start((onDelta, signal) => aiYamlExplain(detailYaml.value, onDelta, signal))
}

// Pod 行内 AI 诊断（列表行/详情实例表共用）：上下文由后端按权限组装（YAML/日志/事件/状态）
function diagnosePod(row: any) {
  aiTitle.value = `AI 诊断 — pod/${row.metadata.name}`
  aiDlgRef.value?.start((onDelta, signal) =>
    aiDiagnose({
      cluster: cluster.value,
      namespace: row.metadata.namespace,
      object_type: 'Pod',
      object_name: row.metadata.name,
    }, onDelta, signal))
}

// 工作负载行内 AI 诊断：后端上下文 = 工作负载 YAML + selector 匹配的关联 Pod 状态/崩溃日志/事件
const workloadKinds: Record<string, string> = {
  deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
  replicasets: 'ReplicaSet', jobs: 'Job', cronjobs: 'CronJob',
}
function diagnoseWorkload(row: any) {
  const kind = row.kind || workloadKinds[cur.resource] || cur.resource
  aiTitle.value = `AI 诊断 — ${kind}/${row.metadata.name}`
  aiDlgRef.value?.start((onDelta, signal) =>
    aiDiagnose({
      cluster: cluster.value,
      namespace: row.metadata.namespace,
      object_type: kind,
      object_name: row.metadata.name,
    }, onDelta, signal))
}

// 生成草稿：流式累积全文，取唯一的 ```yaml 代码块填入部署编辑器（人工审阅后再走部署通道）
async function genYaml() {
  const p = aiGenPrompt.value.trim()
  if (!p) { ElMessage.warning('先描述要生成的资源'); return }
  aiGenerating.value = true
  let full = ''
  try {
    await aiYamlGenerate(p, (d) => { full += d })
    // 只在正文里找代码块：推理模型的思考过程里可能自己起草过 YAML，先剥离再提取
    const body = splitReasoning(full).content || full
    const m = body.match(/```(?:ya?ml)?\s*\n([\s\S]*?)```/)
    if (!m) {
      // 没有代码块（模型不听话/被截断）：原文进编辑器让人工判断
      if (body.trim()) deployYaml.value = body.trim()
      ElMessage.warning('AI 未按格式返回代码块，已填入原始输出，请人工整理')
    } else {
      deployYaml.value = m[1].trimEnd() + '\n'
      ElMessage.success('草稿已生成，请审阅修改后再部署')
    }
  } catch (e) { ElMessage.error(errMsg(e)) } finally { aiGenerating.value = false }
}

// 部署前 AI 审查（只读）：给一句总评 + 按严重度排序的问题清单
const reviewing = ref(false)
function reviewYaml() {
  if (!deployYaml.value.trim()) return
  aiTitle.value = 'AI 部署审查'
  reviewing.value = true
  aiDlgRef.value?.start(async (onDelta, signal) => {
    try {
      await aiYamlReview(deployYaml.value, onDelta, signal)
    } finally {
      reviewing.value = false
    }
  })
}

// ---------- AI 修改（自然语言改 YAML，产出草稿 + diff 人工确认） ----------
// AI 只产出修改后的完整 YAML；应用 = 填入编辑器，生效仍走「保存修改」（权限/审计不变）。
// 输入不做脱敏（值必须原样往返），靠 diff 界面让人核对敏感行未被改动。
const yamlEditable = computed(() =>
  cur.resource !== 'pods' && verbAllowed(authResKey.value, 'update', detailTarget.value?.metadata?.namespace))
const aiEditPrompt = ref('')
const aiEditing = ref(false)
const aiDiffDlg = ref(false)
const aiDiffLines = ref<{ type: 'same' | 'add' | 'del'; text: string }[]>([])
const aiEditedYaml = ref('')

async function doAiYamlEdit() {
  const p = aiEditPrompt.value.trim()
  if (!p) { ElMessage.warning('先输入修改指令'); return }
  if (!detailYaml.value.trim()) { ElMessage.warning('当前没有 YAML 内容可修改'); return }
  aiEditing.value = true
  let full = ''
  try {
    await aiYamlEdit(detailYaml.value, p, (d) => { full += d })
    // 同 genYaml：思考过程里出现过的草稿代码块不算数，只取正文的
    const m = splitReasoning(full).content.match(/```(?:ya?ml)?\s*\n([\s\S]*?)```/)
    if (!m) {
      ElMessage.error('AI 未按格式返回 YAML 代码块，请重试或换一个更明确的指令')
      return
    }
    const edited = m[1].trimEnd() + '\n'
    if (edited === detailYaml.value) {
      ElMessage.info('AI 未做出任何改动（可换个更具体的指令）')
      return
    }
    aiEditedYaml.value = edited
    aiDiffLines.value = lineDiff(detailYaml.value, edited)
    aiDiffDlg.value = true
  } catch (e) { ElMessage.error(errMsg(e)) } finally { aiEditing.value = false }
}

// 应用草稿：只改编辑器内容，真正生效仍需人工点「保存修改」
function applyAiEdit() {
  detailYaml.value = aiEditedYaml.value
  aiDiffDlg.value = false
  aiEditPrompt.value = ''
  ElMessage.success('已应用到编辑器，请核对后点「保存修改」')
}

// ---------- 白屏部署（表单向导） ----------
const fdDlg = ref(false)
const fdDeploying = ref(false)
const fdResults = ref<any[]>([])
const scOptions = ref<string[]>([]) // 存储类选项（懒加载）
const fdAdvOpen = ref<string[]>([]) // 高级选项（存储/Service）折叠状态，默认收起
const fdYamlDlg = ref(false) // YAML 预览弹窗（替代主表单底部常驻预览）

function fdNewProbe() {
  return { type: 'none', path: '/', port: '', scheme: 'HTTP', command: '', initialDelay: 30, period: 10 }
}

function fdNewContainer() {
  return {
    name: 'main', image: '', command: '', args: '', ports: [] as any[],
    open: [] as string[], // 本容器折叠组展开状态（各容器独立）
    res: {
      limitsCpu: { num: '', unit: 'core' }, limitsMem: { num: '', unit: 'Gi' },
      reqCpu: { num: '', unit: 'core' }, reqMem: { num: '', unit: 'Gi' },
      extended: [] as any[],
    },
    env: [] as any[], liveness: fdNewProbe(), readiness: fdNewProbe(),
  }
}

const fd = reactive({
  kind: 'deployment', name: '', namespace: '', replicas: 1, completions: 1, schedule: '*/5 * * * *',
  containers: [fdNewContainer()],
  storage: { enabled: false, storageClass: '', size: '10Gi', accessMode: 'ReadWriteOnce', mountPath: '/data' },
  service: { enabled: false, type: 'ClusterIP', ports: [] as any[] },
  sched: schedModelDefault(), // 调度策略（nodeSelector/节点亲和/Pod 反亲和/nodeName）
})

function fdReset() {
  Object.assign(fd, {
    kind: 'deployment', name: '', namespace: fd.namespace, replicas: 1, completions: 1, schedule: '*/5 * * * *',
    containers: [fdNewContainer()],
    storage: { enabled: false, storageClass: '', size: '10Gi', accessMode: 'ReadWriteOnce', mountPath: '/data' },
    service: { enabled: false, type: 'ClusterIP', ports: [] },
    sched: schedModelDefault(),
  })
  fdResults.value = []
  fdAdvOpen.value = []
}

// ---------- 折叠组标题摘要（纯展示，实时反映已配置内容） ----------
function fdCmdSum(ct: any): string {
  return (ct.command?.trim() || ct.args?.trim()) ? '已配置' : '镜像默认'
}
function fdPortsSum(ct: any): string {
  return ct.ports?.length ? `${ct.ports.length} 个` : '未配置'
}
function fdResSum(ct: any): string {
  const parts: string[] = []
  const req = [qtyStr(ct.res?.reqCpu), qtyStr(ct.res?.reqMem)].filter(Boolean).join('/')
  const lim = [qtyStr(ct.res?.limitsCpu), qtyStr(ct.res?.limitsMem)].filter(Boolean).join('/')
  if (req) parts.push(`请求 ${req}`)
  if (lim) parts.push(`上限 ${lim}`)
  for (const e of ct.res?.extended || []) {
    if (e.key?.trim() && e.value !== '' && e.value != null) parts.push(`${e.key.trim()} ×${e.value}`)
  }
  return parts.length ? parts.join(' · ') : '未配置'
}
function fdEnvSum(ct: any): string {
  return ct.env?.length ? `${ct.env.length} 项` : '未配置'
}
function fdProbeSum(ct: any): string {
  const one = (p: any, label: string) => {
    if (!p || p.type === 'none') return ''
    if (p.type === 'http') return `${label} HTTP${p.port ? ':' + p.port : ''}`
    if (p.type === 'tcp') return `${label} TCP${p.port ? ':' + p.port : ''}`
    return `${label} 命令`
  }
  return [one(ct.liveness, '存活'), one(ct.readiness, '就绪')].filter(Boolean).join(' · ') || '未配置'
}
function fdAdvSum(): string {
  const on: string[] = []
  if (fd.storage.enabled) on.push('存储')
  if (fd.service.enabled) on.push('Service')
  const sched = fdSchedSum()
  if (sched !== '默认调度') on.push('调度策略')
  return on.length ? `已配置 ${on.join(' + ')}` : '未配置'
}
// 调度策略摘要：反映已配置的内容（nodeName / 标签选择 / 亲和行数）
function fdSchedSum(): string {
  const m = fd.sched
  const parts: string[] = []
  if (m.node_name.trim()) parts.push(`节点 ${m.node_name.trim()}`)
  if (m.node_selector.some((r) => r.key.trim())) parts.push(`标签选择 ×${m.node_selector.filter((r) => r.key.trim()).length}`)
  if (m.node_affinity_required.some((r) => r.key.trim())) parts.push('节点亲和')
  if (m.node_affinity_preferred.some((r) => r.key.trim())) parts.push('节点偏好')
  if (m.pod_anti_required.some((r) => r.key.trim())) parts.push('反亲和')
  if (m.pod_anti_preferred.some((r) => r.key.trim())) parts.push('反亲和偏好')
  return parts.length ? parts.join(' · ') : '默认调度'
}
function toggleFdAdvanced(enabled: string | number | boolean) {
  fdAdvOpen.value = enabled ? ['adv'] : []
}

async function loadStorageClasses() {
  if (scOptions.value.length || !cluster.value) return
  try {
    // 存储类为集群级资源，多数用户无 list 授权——静默预加载，失败下拉留空（新建 PVC 时仍可手输）
    const res = await listResources(cluster.value, { group: 'storage.k8s.io', version: 'v1', resource: 'storageclasses', _silent: 1 })
    scOptions.value = (res.data.items || []).map((i: any) => i.metadata.name)
  } catch { scOptions.value = [] }
}

function openFormDeploy() {
  fdReset()
  // 默认选中第一个有权限的类型（fdKindDefs 顺序保证优先 Deployment）
  fd.kind = fdKindOptions.value[0]?.value || 'deployment'
  if (!fd.namespace) fd.namespace = namespace.value || namespaces.value[0]?.name || ''
  fdDlg.value = true
  loadStorageClasses()
  loadNodeOptions() // 调度策略的节点/标签下拉（无权限静默失败，仍可手输）
}

// ---------- 白屏部署：HPA ----------
const hpaDeployDlg = ref(false)
const hpaYamlDlg = ref(false)
const hpaDeploying = ref(false)
const hpaDeployResults = ref<any[]>([])
const hpaTargetOptions = ref<string[]>([])
const hpaDeployForm = reactive({
  name: '', namespace: '', targetKind: 'Deployment', targetName: '',
  minReplicas: 1, maxReplicas: 5, cpu: 60, memory: 0,
})

async function loadHpaTargets() {
  if (!cluster.value || !hpaDeployForm.namespace) {
    hpaTargetOptions.value = []
    return
  }
  const resource = hpaDeployForm.targetKind === 'StatefulSet' ? 'statefulsets' : 'deployments'
  try {
    const res: any = await listResources(cluster.value, {
      group: 'apps', version: 'v1', resource, namespace: hpaDeployForm.namespace, _silent: 1,
    })
    hpaTargetOptions.value = (res.data.items || []).map((i: any) => i.metadata.name).sort()
  } catch {
    // 有 HPA create 权限但无工作负载 list 权限时，仍允许手输目标名
    hpaTargetOptions.value = []
  }
}

function openHpaDeploy() {
  hpaDeployForm.namespace = namespace.value || namespaces.value[0]?.name || ''
  hpaDeployForm.name = ''
  hpaDeployForm.targetKind = 'Deployment'
  hpaDeployForm.targetName = ''
  hpaDeployForm.minReplicas = 1
  hpaDeployForm.maxReplicas = 5
  hpaDeployForm.cpu = 60
  hpaDeployForm.memory = 0
  hpaDeployResults.value = []
  hpaDeployDlg.value = true
  loadHpaTargets()
}

const hpaYaml = computed(() => {
  if (!hpaDeployForm.name.trim()) return '# 请先填写名称，YAML 将自动生成'
  const f = hpaDeployForm
  const metrics: any[] = []
  if (f.cpu > 0) {
    metrics.push({ type: 'Resource', resource: { name: 'cpu', target: { type: 'Utilization', averageUtilization: f.cpu } } })
  }
  if (f.memory > 0) {
    metrics.push({ type: 'Resource', resource: { name: 'memory', target: { type: 'Utilization', averageUtilization: f.memory } } })
  }
  const hpa = {
    apiVersion: 'autoscaling/v2', kind: 'HorizontalPodAutoscaler',
    metadata: { name: f.name.trim(), namespace: f.namespace },
    spec: {
      scaleTargetRef: { apiVersion: 'apps/v1', kind: f.targetKind, name: f.targetName.trim() },
      minReplicas: f.minReplicas, maxReplicas: f.maxReplicas, metrics,
    },
  }
  return yaml.dump(hpa, { lineWidth: -1, noRefs: true })
})

function hpaValidate(): string | null {
  const f = hpaDeployForm
  if (!f.name.trim()) return '请填写 HPA 名称'
  if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(f.name.trim()) || f.name.trim().length > 63) {
    return 'HPA 名称需为小写字母/数字/中划线，且不超过 63 字符'
  }
  if (!f.namespace) return '请选择命名空间'
  if (!f.targetName.trim()) return '请填写目标工作负载名称'
  if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(f.targetName.trim())) {
    return '目标工作负载名称需为小写字母/数字/中划线'
  }
  if (f.minReplicas > f.maxReplicas) return '最小副本不能大于最大副本'
  if (f.cpu <= 0 && f.memory <= 0) return '请至少配置 CPU 或内存阈值'
  return null
}

async function doHpaDeploy(dryRun: boolean) {
  const err = hpaValidate()
  if (err) { ElMessage.warning(err); return }
  hpaDeploying.value = true
  hpaDeployResults.value = []
  try {
    const res = await applyYAML(cluster.value, {
      yaml: hpaYaml.value, dry_run: dryRun, namespace: hpaDeployForm.namespace,
    })
    hpaDeployResults.value = res.data.results || []
    toastResults(hpaDeployResults.value)
    if (!dryRun && res.data.ok) load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    hpaDeploying.value = false
  }
}

// ---------- 白屏部署：ResourceQuota ----------
const quotaDeployDlg = ref(false)
const quotaYamlDlg = ref(false)
const quotaDeploying = ref(false)
const quotaDeployResults = ref<any[]>([])
const quotaKeyOptions = [
  { value: 'requests.cpu', label: 'CPU 请求总量' },
  { value: 'requests.memory', label: '内存请求总量' },
  { value: 'limits.cpu', label: 'CPU 上限总量' },
  { value: 'limits.memory', label: '内存上限总量' },
  { value: 'pods', label: 'Pod 数量' },
  { value: 'persistentvolumeclaims', label: 'PVC 数量' },
  { value: 'requests.storage', label: 'PVC 存储总量' },
  { value: 'services', label: 'Service 数量' },
  { value: 'configmaps', label: 'ConfigMap 数量' },
  { value: 'secrets', label: 'Secret 数量' },
]
const quotaDeployForm = reactive({
  name: '', namespace: '', items: [] as Array<{ key: string; value: string }>,
})

function addQuotaItem() {
  const used = new Set(quotaDeployForm.items.map((i) => i.key))
  const next = quotaKeyOptions.find((k) => !used.has(k.value))?.value || 'pods'
  quotaDeployForm.items.push({ key: next, value: '' })
}

function openQuotaDeploy() {
  quotaDeployForm.name = ''
  quotaDeployForm.namespace = namespace.value || namespaces.value[0]?.name || ''
  quotaDeployForm.items = [
    { key: 'requests.cpu', value: '' },
    { key: 'requests.memory', value: '' },
    { key: 'limits.cpu', value: '' },
    { key: 'limits.memory', value: '' },
  ]
  quotaDeployResults.value = []
  quotaDeployDlg.value = true
}

function quotaHard(): Record<string, string> {
  const hard: Record<string, string> = {}
  for (const item of quotaDeployForm.items) {
    if (item.key && item.value.trim()) hard[item.key] = item.value.trim()
  }
  return hard
}

const quotaYaml = computed(() => {
  if (!quotaDeployForm.name.trim()) return '# 请先填写名称，YAML 将自动生成'
  const quota = {
    apiVersion: 'v1', kind: 'ResourceQuota',
    metadata: { name: quotaDeployForm.name.trim(), namespace: quotaDeployForm.namespace },
    spec: { hard: quotaHard() },
  }
  return yaml.dump(quota, { lineWidth: -1, noRefs: true })
})

function quotaValueError(key: string, value: string): string | null {
  if (key === 'requests.cpu' || key === 'limits.cpu') {
    return /^\d+(\.\d+)?m?$/.test(value) ? null : 'CPU 配额需为数字，可带 m，如 500m / 2'
  }
  if (key === 'requests.memory' || key === 'limits.memory' || key === 'requests.storage') {
    return /^\d+(\.\d+)?(Ki|Mi|Gi|Ti|Pi|E|k|M|G|T|P|E)?$/.test(value)
      ? null : '容量配额需为数字加单位，如 512Mi / 2Gi'
  }
  return /^\d+$/.test(value) ? null : '数量配额需为正整数'
}

function quotaValidate(): string | null {
  const f = quotaDeployForm
  if (!f.name.trim()) return '请填写 ResourceQuota 名称'
  if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(f.name.trim()) || f.name.trim().length > 63) {
    return 'ResourceQuota 名称需为小写字母/数字/中划线，且不超过 63 字符'
  }
  if (!f.namespace) return '请选择命名空间'
  const rows = f.items.filter((i) => i.key || i.value.trim())
  if (!rows.length) return '请至少配置一项配额'
  const seen = new Set<string>()
  for (const [i, item] of rows.entries()) {
    if (!item.key) return `第 ${i + 1} 项配额未选择资源`
    if (seen.has(item.key)) return `配额项重复：${item.key}`
    seen.add(item.key)
    if (!item.value.trim()) return `请填写 ${item.key} 的上限`
    const valueErr = quotaValueError(item.key, item.value.trim())
    if (valueErr) return `${item.key}：${valueErr}`
  }
  const hard = quotaHard()
  if (('limits.cpu' in hard) && !('requests.cpu' in hard)) {
    return '配置 CPU 上限总量时，需同时配置 CPU 请求总量'
  }
  if (('limits.memory' in hard) && !('requests.memory' in hard)) {
    return '配置内存上限总量时，需同时配置内存请求总量'
  }
  return null
}

async function doQuotaDeploy(dryRun: boolean) {
  const err = quotaValidate()
  if (err) { ElMessage.warning(err); return }
  quotaDeploying.value = true
  quotaDeployResults.value = []
  try {
    const res = await applyYAML(cluster.value, {
      yaml: quotaYaml.value, dry_run: dryRun, namespace: quotaDeployForm.namespace,
    })
    quotaDeployResults.value = res.data.results || []
    toastResults(quotaDeployResults.value)
    if (!dryRun && res.data.ok) load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    quotaDeploying.value = false
  }
}

// 类型列表变化（如弹窗开着时切换集群）：当前选中被过滤掉则自动切回第一项
watch(fdKindOptions, (opts) => {
  if (fdDlg.value && !opts.some((k) => k.value === fd.kind)) {
    fd.kind = opts[0]?.value || 'deployment'
  }
})

// 开启 Service 时若端口为空，自动从容器端口填充
function fdOnService(on: any) {
  if (on && !fd.service.ports.length) fdFillSvcPorts()
}
function fdOnStorage(_on: any) { /* 预留 */ }

function fdFillSvcPorts() {
  const all = fd.containers.flatMap((c: any) => c.ports || []).filter((p: any) => p.containerPort)
  if (!all.length) { ElMessage.info('容器还未配置端口，请先在容器区添加'); return }
  fd.service.ports = all.map((p: any) => ({ name: p.name || '', port: p.containerPort, targetPort: p.containerPort, nodePort: null }))
}

// 探针表单 → k8s probe 对象
function fdProbeObj(p: any): any | undefined {
  if (!p || p.type === 'none') return undefined
  const base: any = {}
  if (p.initialDelay) base.initialDelaySeconds = p.initialDelay
  if (p.period) base.periodSeconds = p.period
  if (p.type === 'http') return { ...base, httpGet: { path: p.path || '/', port: portVal(p.port) || 80, scheme: p.scheme === 'HTTPS' ? 'HTTPS' : 'HTTP' } }
  if (p.type === 'tcp') return { ...base, tcpSocket: { port: portVal(p.port) || 80 } }
  return { ...base, exec: { command: String(p.command || '').split(/\s+/).filter(Boolean) } }
}

// 简易 shell 分词：空格分隔，双引号/单引号可包住含空格的参数（如 sh -c "echo hi"）
function shSplit(s: string): string[] {
  const out: string[] = []
  for (const m of String(s).matchAll(/"([^"]*)"|'([^']*)'|(\S+)/g)) {
    out.push(m[1] ?? m[2] ?? m[3])
  }
  return out
}

// 入口是否 shell（sh/bash/dash/zsh 等，basename 以 sh 结尾）
function isShellEntry(entryParts: string[]): boolean {
  return /sh$/.test((entryParts[0] || '').split('/').pop() || '')
}

// 入口 + 命令 → k8s command/args：
// - 入口为 shell（如 /bin/sh）或留空自动包装（fd 创建）：命令整段（多行 = 多条依序执行）作为 -c 脚本，免手写 -c
// - 入口为非 shell 程序（如 nginx）或留空（编辑模式，保持镜像 CMD 语义）：命令每行一个参数（空格原样保留）
//   （autoWrap=false 用于编辑抽屉，保证存量对象回填/保存闭环）
function cmdPartsFromForm(entryStr: string, cmdText: string, autoWrap: boolean): { command?: string[]; args?: string[] } {
  const entry = String(entryStr || '').trim()
  const text = String(cmdText || '').trim()
  const out: { command?: string[]; args?: string[] } = {}
  const lineArgs = text.split('\n').map(l => l.trim()).filter(Boolean)
  if (!entry) {
    if (text) {
      if (autoWrap) { out.command = ['/bin/sh', '-c']; out.args = [text] }
      else out.args = lineArgs
    }
    return out
  }
  const parts = shSplit(entry).filter(Boolean)
  const shell = isShellEntry(parts)
  if (shell) {
    if (text && !parts.includes('-c')) parts.push('-c')
    out.command = parts
    if (text) out.args = [text]
  } else {
    out.command = parts
    if (text) out.args = lineArgs
  }
  return out
}

// k8s command/args → 表单字段（回填，不合成引号）：
// shell + args[0]=='-c' 的脚本模式还原为「入口 /bin/sh + 多行命令原文」；其余命令每行一个参数（原样）
function cmdFormVals(cmds: any, args: any): { command: string; args: string } {
  const q = (s: string) => /\s/.test(s) ? `"${s}"` : s
  const c: string[] = cmds || []
  const a: string[] = args || []
  if (isShellEntry(c) && a[0] === '-c') {
    return { command: c.filter(p => p !== '-c').map(q).join(' '), args: a.slice(1).join('\n') }
  }
  return { command: c.map(q).join(' '), args: a.join('\n') }
}

// 容器表单 → k8s container 对象
function fdContainerObj(ct: any): any {
  const c: any = { name: ct.name.trim(), image: ct.image.trim() }
  const cp = cmdPartsFromForm(ct.command || '', ct.args || '', true)
  if (cp.command) c.command = cp.command
  if (cp.args) c.args = cp.args
  const ports = (ct.ports || []).filter((p: any) => p.containerPort)
  if (ports.length) {
    c.ports = ports.map((p: any) => ({
      ...(p.name ? { name: p.name } : {}), containerPort: p.containerPort,
      ...(p.hostPort ? { hostPort: p.hostPort } : {}), protocol: p.protocol || 'TCP',
    }))
  }
  const lim: any = {}, req: any = {}
  const lc = qtyStr(ct.res.limitsCpu); if (lc) lim.cpu = lc
  const lm = qtyStr(ct.res.limitsMem); if (lm) lim.memory = lm
  const rc = qtyStr(ct.res.reqCpu); if (rc) req.cpu = rc
  const rm = qtyStr(ct.res.reqMem); if (rm) req.memory = rm
  // 异构资源（nvidia.com/gpu / huawei.com/npu 等）计入 limits
  for (const e of ct.res.extended || []) {
    if (e.key?.trim() && e.value !== '' && e.value != null) lim[e.key.trim()] = e.value
  }
  const res: any = {}
  if (Object.keys(lim).length) res.limits = lim
  if (Object.keys(req).length) res.requests = req
  if (Object.keys(res).length) c.resources = res
  const env = (ct.env || []).filter((e: any) => e.name?.trim()).map((e: any) => {
    if (e.type === 'secret') return { name: e.name.trim(), valueFrom: { secretKeyRef: { name: e.refName?.trim() || '', key: e.refKey?.trim() || '' } } }
    if (e.type === 'configmap') return { name: e.name.trim(), valueFrom: { configMapKeyRef: { name: e.refName?.trim() || '', key: e.refKey?.trim() || '' } } }
    if (e.type === 'fieldref') return { name: e.name.trim(), valueFrom: { fieldRef: { apiVersion: 'v1', fieldPath: e.refKey?.trim() || 'metadata.name' } } }
    return { name: e.name.trim(), value: e.value ?? '' }
  })
  if (env.length) c.env = env
  const lp = fdProbeObj(ct.liveness)
  if (lp) c.livenessProbe = lp
  const rp = fdProbeObj(ct.readiness)
  if (rp) c.readinessProbe = rp
  return c
}

// 全表单 → k8s 对象数组（工作负载 [+ PVC] [+ Service]）
function buildFdObjects(): any[] {
  const name = fd.name.trim()
  const labels = { app: name }
  const pvcName = `${name}-data`
  const mountPath = fd.storage.mountPath?.trim()
  const useStorage = fd.storage.enabled && !!mountPath
  const containers = fd.containers.map(fdContainerObj)
  if (useStorage) {
    for (const c of containers) c.volumeMounts = [{ name: pvcName, mountPath }]
  }
  const pvcSpec = () => ({
    accessModes: [fd.storage.accessMode],
    ...(fd.storage.storageClass ? { storageClassName: fd.storage.storageClass } : {}),
    resources: { requests: { storage: fd.storage.size || '10Gi' } },
  })
  const podSpec: any = { containers, ...schedToK8s(fd.sched) }
  if (useStorage && fd.kind !== 'statefulset') {
    podSpec.volumes = [{
      name: pvcName,
      persistentVolumeClaim: { claimName: pvcName },
    }]
  }
  const podTemplate = { metadata: { labels }, spec: podSpec }

  let workload: any
  if (fd.kind === 'deployment') {
    workload = {
      apiVersion: 'apps/v1', kind: 'Deployment',
      metadata: fdMeta(name), spec: {
        replicas: fd.replicas, selector: { matchLabels: labels }, template: podTemplate,
      },
    }
  } else if (fd.kind === 'statefulset') {
    const spec: any = {
      serviceName: name, replicas: fd.replicas,
      selector: { matchLabels: labels }, template: podTemplate,
    }
    if (useStorage) {
      spec.volumeClaimTemplates = [{ metadata: { name: pvcName }, spec: pvcSpec() }]
    }
    workload = { apiVersion: 'apps/v1', kind: 'StatefulSet', metadata: fdMeta(name), spec }
  } else if (fd.kind === 'daemonset') {
    // DaemonSet 副本由调度器按节点管理，spec 不含 replicas
    workload = {
      apiVersion: 'apps/v1', kind: 'DaemonSet', metadata: fdMeta(name),
      spec: { selector: { matchLabels: labels }, template: podTemplate },
    }
  } else {
    podSpec.restartPolicy = 'Never'
    const jobSpec = { completions: fd.completions, template: podTemplate }
    if (fd.kind === 'job') {
      workload = { apiVersion: 'batch/v1', kind: 'Job', metadata: fdMeta(name), spec: jobSpec }
    } else {
      workload = {
        apiVersion: 'batch/v1', kind: 'CronJob', metadata: fdMeta(name),
        spec: { schedule: fd.schedule.trim(), jobTemplate: { spec: jobSpec } },
      }
    }
  }
  const objects: any[] = []
  if (useStorage && fd.kind !== 'statefulset') {
    objects.push({ apiVersion: 'v1', kind: 'PersistentVolumeClaim', metadata: fdMeta(pvcName), spec: pvcSpec() })
  }
  objects.push(workload)
  if (fd.service.enabled) {
    const ports = fd.service.ports.filter((p: any) => p.port)
    objects.push({
      apiVersion: 'v1', kind: 'Service', metadata: fdMeta(name),
      spec: {
        type: fd.service.type, selector: labels,
        ports: ports.map((p: any) => ({
          ...(p.name ? { name: p.name } : {}),
          port: p.port, targetPort: portVal(p.targetPort) ?? p.port,
          ...(fd.service.type === 'NodePort' && p.nodePort ? { nodePort: p.nodePort } : {}),
        })),
      },
    })
  }
  return objects
}

function fdMeta(name: string) {
  return { name, namespace: fd.namespace, labels: { app: fd.name.trim() } }
}

const fdYaml = computed(() => {
  if (!fd.name.trim()) return '# 请先填写名称，YAML 将自动生成'
  try {
    return buildFdObjects().map((o) => yaml.dump(o, { lineWidth: -1, noRefs: true })).join('---\n')
  } catch {
    return '# YAML 生成失败，请检查表单内容'
  }
})

// 白屏部署防呆校验：返回第一条错误文案（null = 通过）。试运行/部署前都会先走这里，
// 避免明显非法的表单打到 apiserver 才报一长串英文错误。
function fdValidate(): string | null {
  const name = fd.name.trim()
  if (!name) return '请填写名称'
  // 名称会同时用作 app 标签值，标签值上限 63 字符
  if (name.length > 63) return `名称过长（${name.length} 字符，上限 63）`
  if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(name)) {
    return '名称需符合 k8s 规范：小写字母/数字/中划线，且以字母或数字开头结尾'
  }
  if (!fd.namespace) return '请选择命名空间'
  if (fd.kind === 'cronjob') {
    const parts = fd.schedule.trim().split(/\s+/)
    if (parts.length !== 5) return 'Cron 表达式需为 5 段（分 时 日 月 周），如 */5 * * * *'
    if (parts.some((p: string) => !/^[\d*/,-]+$/.test(p))) return 'Cron 表达式含非法字符（仅支持数字 * / , -）'
  }
  const seen = new Set()
  for (let i = 0; i < fd.containers.length; i++) {
    const ct = fd.containers[i]
    const tag = fd.containers.length > 1 ? `容器 ${i + 1}（${ct.name.trim() || '未命名'}）：` : ''
    if (!ct.name.trim()) return '请填写容器名'
    if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(ct.name.trim()) || ct.name.trim().length > 63) {
      return `${tag}容器名需为小写字母/数字/中划线（≤63 字符，字母或数字开头结尾）`
    }
    if (seen.has(ct.name.trim())) return `${tag}容器名与其他容器重复`
    seen.add(ct.name.trim())
    const img = ct.image.trim()
    if (!img) return '请填写容器镜像'
    if (/\s/.test(img) || !/^[\x21-\x7e]+$/.test(img)) return `${tag}镜像地址含空格或非 ASCII 字符`
    if (!/^[a-z0-9._\-/:@]+$/.test(img)) return `${tag}镜像地址格式不对（应为 registry/repo:tag 形式）`
    // 入口按空格分词，引号不闭合会被静默吞掉，提前拦下；命令无需引号（脚本原样 / 每行一个参数）
    const cmdEntry = String(ct.command || '').trim()
    if ((cmdEntry.match(/"/g)?.length || 0) % 2 || (cmdEntry.match(/'/g)?.length || 0) % 2) {
      return `${tag}入口 (Entrypoint) 的引号未闭合`
    }
    for (const e of ct.env || []) {
      if (!e.name?.trim()) continue // 整行没填名的忽略
      if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(e.name.trim())) {
        return `${tag}环境变量名不合法「${e.name}」，需字母开头且仅含字母/数字/下划线`
      }
    }
    const nums = [ct.res.reqCpu, ct.res.limitsCpu, ct.res.reqMem, ct.res.limitsMem]
    if (nums.some((n: any) => n.num !== '' && n.num != null && !/^\d+(\.\d+)?$/.test(String(n.num)))) {
      return `${tag}资源配置的数值需为数字（如 0.5 / 2）`
    }
    const boundsError = fdResourceBoundsError(ct)
    if (boundsError) return `${tag}${boundsError}`
    for (const ex of ct.res.extended || []) {
      if (ex.key?.trim() && (ex.value === '' || ex.value == null || !/^\d+$/.test(String(ex.value)))) {
        return `${tag}异构资源「${ex.key}」的数量需为整数`
      }
    }
    for (const p of ct.ports || []) {
      if (!p.name) continue
      if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(p.name) || p.name.length > 15) {
        return `${tag}端口名「${p.name}」需为小写字母/数字/中划线且不超过 15 字符`
      }
    }
  }
  if (fd.storage.enabled) {
    if (`${name}-data`.length > 63) return '启用存储时自动生成的卷名超过 63 个字符，请缩短应用名称'
    if (!fd.storage.mountPath?.trim()) return '已启用存储，请填写挂载路径'
    if (!fd.storage.mountPath.trim().startsWith('/')) return '挂载路径需以 / 开头'
    if (fd.storage.mountPath.trim() === '/') return '挂载路径不能是根目录 /'
    if (!/^\d+(\.\d+)?(Ki|Mi|Gi|Ti|Pi|E|k|M|G|T|P|E)?$/.test(fd.storage.size.trim())) {
      return '存储卷容量格式不对，如 10Gi / 512Mi（纯数字默认字节）'
    }
  }
  if (fd.service.enabled && !fd.service.ports.some((p: any) => p.port)) {
    return '已开启 Service，请至少配置一个服务端口'
  }
  const schedErr = validateSched(fd.sched)
  if (schedErr) return `调度策略：${schedErr}`
  return null
}

async function doFormDeploy(dryRun: boolean) {
  const err = fdValidate()
  if (err) { ElMessage.warning(err); return }
  // 软提示不拦截：镜像未固定 tag 会拉 latest，生产环境容易出事故
  const noTag = fd.containers
    .filter((c: any) => c.image.trim() && !c.image.trim().split('/').pop()!.includes(':'))
    .map((c: any) => c.image.trim())
  if (noTag.length) {
    ElMessage.warning(`镜像 ${noTag.join('、')} 未指定 tag，将拉取 latest，建议固定版本`)
  }
  fdDeploying.value = true
  fdResults.value = []
  try {
    const res = await applyYAML(cluster.value, { yaml: fdYaml.value, dry_run: dryRun, namespace: fd.namespace })
    fdResults.value = res.data.results || []
    toastResults(fdResults.value)
    if (!dryRun && res.data.ok) load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { fdDeploying.value = false }
}

// 日志 / 终端
const logsDlg = ref(false)
const logsTarget = ref<any>(null)
const logContainer = ref('') // 当前查看日志的容器（多容器 Pod 在对话框标题栏切换）
const termDlg = ref(false)
const termTarget = ref<any>(null)
const termContainer = ref('') // 当前终端会话的容器（切换 = 重开会话，:key 强制重建组件）

function nodeReady(row: any): string {
  const c = (row.status?.conditions || []).find((x: any) => x.type === 'Ready')
  return c?.status === 'True' ? 'success' : 'danger'
}

// ---------- Node 专属：角色 + CPU/内存/扩展资源（GPU/NPU/DCU…）----------
// 基础资源之外的 capacity 键一律按扩展资源展示（与后端 h_nodesummary.go isBaseNodeResource 同口径），新增设备无需改代码
const BASE_NODE_RES = new Set(['cpu', 'memory', 'pods', 'ephemeral-storage'])
function isExtResKey(k: string): boolean {
  return !BASE_NODE_RES.has(k) && !k.startsWith('hugepages-') && !k.startsWith('attachable-volumes-')
}
function nodeRolesOf(row: any): string[] {
  const prefix = 'node-role.kubernetes.io/'
  const roles = Object.keys(row.metadata?.labels || {}).filter((k) => k.startsWith(prefix)).map((k) => k.slice(prefix.length))
  return roles.length ? roles.sort() : ['worker'] // 无角色标签 = worker（kubectl 口径）
}
// 节点主显示名：历史扩容节点命名不规范（有主机名有 IP），列表/标题统一展示内网 IP，取不到时回退 metadata.name
function nodeIp(row: any): string {
  const a = (row.status?.addresses || []).find((x: any) => x.type === 'InternalIP')
  return a?.address || ''
}
// 扩展资源短名：nvidia.com/gpu → gpu（tooltip 显示全名）
function extResShort(name: string): string {
  const i = name.lastIndexOf('/')
  return i >= 0 ? name.slice(i + 1) : name
}
// K8s quantity 字符串 → 数值（cpu: 核；内存/存储: 字节；扩展资源: 个数）。
// 与表单编辑的 parseQty(v, kind)（拆数值+单位字符串）用途不同，故独立命名。
function parseQuantity(s?: string): number {
  if (!s) return 0
  const m = String(s).trim().match(/^([\d.]+)\s*([a-zA-Z]*)$/)
  if (!m) return 0
  const UNITS: Record<string, number> = {
    '': 1, m: 0.001, k: 1e3, M: 1e6, G: 1e9, T: 1e12, P: 1e15, E: 1e18,
    Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4, Pi: 1024 ** 5, Ei: 1024 ** 6,
  }
  return parseFloat(m[1]) * (UNITS[m[2]] ?? 1)
}
function nodeCpuCores(row: any): number { return parseQuantity(row.status?.capacity?.cpu) }
function nodeMemBytes(row: any): number { return parseQuantity(row.status?.capacity?.memory) }
function nodeExtRes(row: any): { name: string; short: string; count: string }[] {
  return Object.entries(row.status?.capacity || {})
    .filter(([k]) => isExtResKey(k))
    .map(([name, v]) => ({ name, short: extResShort(name), count: String(v) }))
}
function fmtCores(v: number): string { return `${+v.toFixed(v % 1 ? 1 : 0)}C` }
function fmtGi(bytes: number): string { return `${+(bytes / 1024 ** 3).toFixed(bytes % 1024 ** 3 ? 1 : 0)}Gi` }
function fmtMilli(m: number): string { return m ? fmtCores(m / 1000) : '0' }
function fmtMemBytes(b: number): string { return b ? fmtGi(b) : '0' }
function fmtResQty(unit: string, q?: string): string {
  if (!q) return '-'
  if (unit === 'milli') return fmtCores(parseQuantity(q))
  if (unit === 'bytes') return fmtGi(parseQuantity(q))
  return String(parseQuantity(q))
}
function fmtAllocated(unit: string, v: number): string {
  if (unit === 'milli') return fmtCores(v / 1000)
  if (unit === 'bytes') return fmtGi(v)
  return String(v)
}
function resKindLabel(kind: string): string {
  return ({ cpu: 'CPU', memory: '内存', pods: 'Pods', storage: '临时存储' } as Record<string, string>)[kind] || kind
}
function allocPctOf(row: any): number {
  const den = parseQuantity(row.allocatable)
  if (!den) return 0
  const num = row.unit === 'milli' ? row.allocated / 1000 : row.allocated
  return Math.min(100, Math.round((num / den) * 100))
}
const pctColor = (p: number) => (p > 80 ? '#F56C6C' : p > 60 ? '#E6A23C' : '#67C23A')
function sortNodeCpu(a: any, b: any): number { return nodeCpuCores(a) - nodeCpuCores(b) }

// ---------- Node 概要（详情抽屉「概要」页签数据） ----------
const overview = ref<any>(null)
const overviewLoading = ref(false)
const overviewErr = ref('')

async function loadOverview() {
  const row = detailTarget.value
  if (!row || !cluster.value) return
  overviewLoading.value = true
  overviewErr.value = ''
  try {
    const res: any = await nodeSummary(cluster.value, row.metadata.name)
    overview.value = res.data
  } catch { /* http 层已提示 */ } finally { overviewLoading.value = false }
}

// 节点监控：Grafana var-node 变量（与 Pod 的 openGrafana 同一集群监控地址）
function openGrafanaNode() {
  const base = grafanaBase.value
  if (!base || !detailTarget.value) return
  const hasQ = base.includes('?')
  const sep = /[?&#]$/.test(base) ? '' : (hasQ ? '&' : '?')
  window.open(`${base}${sep}var-node=${encodeURIComponent(detailTarget.value.metadata.name)}`, '_blank', 'noopener')
}

// ---------- 节点运维：封锁/恢复调度、排空、删除（双确认） ----------
const nodeOpsLoading = ref(false)

// cordon/uncordon：轻量可逆，普通确认提示影响即可；成功后同步详情对象 + 概要 + 列表
async function doNodeCordon(unschedulable: boolean, row = detailTarget.value) {
  const name = row?.metadata?.name
  if (!name) return
  try {
    await ElMessageBox.confirm(
      unschedulable
        ? `封锁后新 Pod 不会调度到节点 "${name}"，存量 Pod 不受影响。可随时恢复调度。`
        : `恢复后节点 "${name}" 重新参与调度。`,
      unschedulable ? '封锁调度确认' : '恢复调度确认', { type: 'warning', confirmButtonText: '确定', cancelButtonText: '取消' },
    )
  } catch { return }
  nodeOpsLoading.value = true
  try {
    await nodeCordon(cluster.value, name, unschedulable)
    ElMessage.success(unschedulable ? '已封锁调度' : '已恢复调度')
    if (row) row.spec = { ...row.spec, unschedulable }
    if (detailTarget.value?.metadata?.name === name) loadOverview()
    load(true)
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { nodeOpsLoading.value = false }
}

// 排空对话框三步状态机：options（勾选项）→ review（排空计划确认）→ run（逐 Pod 驱动 + 可中止）。
// 执行侧由前端驱动：先（可选）封锁调度，再逐个调 evict——每发一个请求卡片翻绿，中止即停止发后续请求
const drainDlg = ref(false)
const drainTargetName = ref('')
const drainStep = ref<'options' | 'review' | 'run'>('options')
const drainPlanning = ref(false)
const drainForm = reactive({
  cordon: true,
  include_daemonsets: false,
  force_bare_pods: false,
  grace_seconds: 30,
})

// 排空计划（review 步展示）与执行状态（run 步展示）
const drainPlan = ref<{ evictable: any[]; skipped: { name: string; reason: string }[] }>({ evictable: [], skipped: [] })
// 计划返回的节点封锁状态：完成提示以实际为准（节点可能本来就是封锁的，用户未勾封锁也是已封锁）
const drainNodeUnschedulable = ref(false)
// 确认步的提示条：封锁后复核发现计划变化时展示（新 Pod 待重新确认），重新获取计划时清空
const drainReviewNote = ref('')
interface DrainRunItem { uid: string; namespace: string; name: string; state: 'pending' | 'running' | 'done' | 'failed' | 'cancelled'; err?: string }
const drainRunItems = ref<DrainRunItem[]>([])
const drainAbort = ref(false)
// 已结算数（成功/失败/已中止）：中止把剩余 pending 置为 cancelled，进度条与完成判定随之收敛
const drainDoneCount = computed(() => drainRunItems.value.filter((p) => p.state === 'done' || p.state === 'failed' || p.state === 'cancelled').length)
const drainFailCount = computed(() => drainRunItems.value.filter((p) => p.state === 'failed').length)
const drainCancelCount = computed(() => drainRunItems.value.filter((p) => p.state === 'cancelled').length)
const drainRunFinished = computed(() => drainRunItems.value.length > 0 && drainDoneCount.value === drainRunItems.value.length)

function openDrainDlg(row = detailTarget.value) {
  drainTargetName.value = row?.metadata?.name || ''
  if (!drainTargetName.value) return
  drainStep.value = 'options'
  drainPlan.value = { evictable: [], skipped: [] }
  drainNodeUnschedulable.value = false
  drainReviewNote.value = ''
  drainRunItems.value = []
  drainAbort.value = false
  drainDlg.value = true
}

function skipReasonLabel(r: string): string {
  switch (r) {
    case 'daemonset': return 'DaemonSet Pod（未勾选驱逐）'
    case 'bare': return '裸 Pod（无控制器，未勾选删除）'
    case 'mirror': return '静态 Pod（kubelet 托管，不可驱逐）'
    case 'no-permission': return '无 pods 删除权限'
    default: return r
  }
}

function drainCardStateLabel(s: string): string {
  switch (s) {
    case 'pending': return '等待驱逐'
    case 'running': return '驱逐中…'
    case 'done': return '已驱逐'
    case 'failed': return '驱逐失败'
    case 'cancelled': return '已中止（留在节点）'
    default: return s
  }
}

// 获取排空计划（只读）：后端按勾选项分类，前端弹确认列表
async function fetchDrainPlan() {
  const name = drainTargetName.value
  if (!name) return
  drainPlanning.value = true
  try {
    const res: any = await nodeDrainPlan(cluster.value, name, {
      include_daemonsets: drainForm.include_daemonsets,
      force_bare_pods: drainForm.force_bare_pods,
    })
    drainPlan.value = { evictable: res.data.evictable || [], skipped: res.data.skipped || [] }
    drainNodeUnschedulable.value = !!res.data.node_unschedulable
    drainReviewNote.value = ''
    drainStep.value = 'review'
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { drainPlanning.value = false }
}

// 开始执行：（可选）封锁调度 → 逐 Pod evict（动效 + 中止）。网络错按 failed 继续下一个
async function startDrainRun() {
  const name = drainTargetName.value
  if (!name) return
  drainRunItems.value = drainPlan.value.evictable.map((p) => ({
    uid: p.uid, namespace: p.namespace, name: p.name, state: 'pending' as const,
  }))
  drainAbort.value = false
  drainStep.value = 'run'
  // 前置封锁（勾选时）：失败则整批失败提示，不逐个硬驱逐
  if (drainForm.cordon) {
    try {
      await nodeCordon(cluster.value, name, true)
      if (detailTarget.value?.metadata?.name === name) detailTarget.value.spec = { ...detailTarget.value.spec, unschedulable: true }
    } catch (e) {
      ElMessage.error('封锁调度失败，已停止排空: ' + errMsg(e))
      drainRunItems.value = []
      drainStep.value = 'review'
      return
    }
    // 封锁后复核完整计划：可驱逐 Pod 的增删或跳过列表变化都需要用户重新确认；
    // 复核失败同样回到确认步明确提示，不按过期计划盲跑
    try {
      const re: any = await nodeDrainPlan(cluster.value, name, {
        include_daemonsets: drainForm.include_daemonsets,
        force_bare_pods: drainForm.force_bare_pods,
      })
      const previousUIDs = new Set(drainRunItems.value.map((p) => p.uid))
      const fresh = (re.data.evictable || []) as any[]
      const freshUIDs = new Set(fresh.map((p) => p.uid))
      const addedCount = [...freshUIDs].filter((uid) => !previousUIDs.has(uid)).length
      const removedCount = [...previousUIDs].filter((uid) => !freshUIDs.has(uid)).length
      const previousSkipped = new Set(drainPlan.value.skipped.map((p) => `${p.name}\u0000${p.reason}`))
      const freshSkipped = (re.data.skipped || []) as { name: string; reason: string }[]
      const freshSkippedKeys = new Set(freshSkipped.map((p) => `${p.name}\u0000${p.reason}`))
      const skippedChanged = previousSkipped.size !== freshSkippedKeys.size ||
        [...previousSkipped].some((key) => !freshSkippedKeys.has(key))
      if (addedCount || removedCount || skippedChanged) {
        drainPlan.value = { evictable: fresh, skipped: freshSkipped }
        drainNodeUnschedulable.value = !!re.data.node_unschedulable
        drainRunItems.value = []
        const changes = []
        if (addedCount) changes.push(`新增 ${addedCount} 个可驱逐 Pod`)
        if (removedCount) changes.push(`${removedCount} 个原计划 Pod 不再属于可驱逐列表`)
        if (skippedChanged) changes.push('跳过列表也有变化')
        drainReviewNote.value = `封锁后排空计划发生变化（${changes.join('；')}）。已更新完整列表，请重新确认。`
        drainStep.value = 'review'
        return
      }
    } catch (e) {
      drainRunItems.value = []
      drainReviewNote.value = ''
      drainStep.value = 'review'
      ElMessage.error('封锁后复核排空计划失败: ' + errMsg(e) + '，请「返回调整」重新获取计划后再执行')
      return
    }
  }
  for (const p of drainRunItems.value) {
    if (drainAbort.value) break // 中止：剩余 Pod 保持 pending 留在节点
    p.state = 'running'
    try {
      await nodeEvict(cluster.value, name, {
        namespace: p.namespace, name: p.name, uid: p.uid, grace_seconds: drainForm.grace_seconds,
      })
      p.state = 'done'
    } catch (e) {
      p.state = 'failed'
      p.err = errMsg(e)
    }
  }
  // 中止收尾：未处理的 Pod 置为 cancelled（进度/完成按钮随之收敛，对话框可正常关闭）
  if (drainAbort.value) {
    for (const p of drainRunItems.value) {
      if (p.state === 'pending') p.state = 'cancelled'
    }
  }
  loadOverview()
  load(true)
  if (drainAbort.value) {
    ElMessage.warning(`排空已中止：${drainCancelCount.value} 个 Pod 未处理（留在节点）`)
  } else if (drainFailCount.value) {
    ElMessage.warning('排空完成，有 Pod 驱逐失败')
  } else {
    ElMessage.success('排空完成')
  }
}

// 删除节点：从列表收进详情，双确认（输名称 + Pod 数/不可恢复警示）
async function doDeleteNode() {
  const name = detailTarget.value?.metadata?.name
  if (!name) return
  try {
    await ElMessageBox.prompt(
      `该操作不可恢复。请输入节点名 "${name}" 以确认删除：`,
      '删除节点 - 第一次确认', {
        type: 'warning', confirmButtonText: '下一步', cancelButtonText: '取消',
        inputValidator: (v: string) => v.trim() === name || '输入的节点名不匹配',
      },
    )
    const podCount = overview.value?.pod_count ?? 0
    await ElMessageBox.confirm(
      `节点 "${name}" 上有 ${podCount} 个 Pod（平台可见口径）。` +
      'Etcd 中节点对象将被移除，节点上的工作负载会迁移或中断，不可恢复。',
      '删除节点 - 第二次确认', { type: 'error', confirmButtonText: '我已知晓，删除', cancelButtonText: '取消' },
    )
  } catch { return }
  try {
    await deleteResource(cluster.value, {
      group: '', version: 'v1', resource: 'nodes',
      namespace: '', name,
    })
    ElMessage.success('节点已删除')
    detailDlg.value = false
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

// ---------- Ingress 专属：host / path 路由 ----------
type ingRoute = { host: string; path: string; svc: string; port: string }
function ingRoutes(row: any): ingRoute[] {
  const out: ingRoute[] = []
  for (const r of row.spec?.rules || []) {
    for (const p of r?.http?.paths || []) {
      const svc = p?.backend?.service
      out.push({
        host: r.host || '*',
        path: p.path || '/',
        svc: svc?.name || p?.backend?.resource?.kind || '—',
        port: String(svc?.port?.number ?? svc?.port?.name ?? ''),
      })
    }
  }
  // 默认后端型（无 rules、只有 spec.defaultBackend）
  if (!out.length) {
    const svc = row.spec?.defaultBackend?.service
    if (svc?.name) out.push({ host: '*', path: '/', svc: svc.name, port: String(svc?.port?.number ?? svc?.port?.name ?? '') })
  }
  return out
}
function ingHosts(row: any): string[] {
  const seen: string[] = []
  for (const r of ingRoutes(row)) if (!seen.includes(r.host)) seen.push(r.host)
  return seen
}
// 渲染行：同 host 的后续行 showHost=false（host 位置留白，视觉分组对齐）；key 加序号防同路由重复
function ingRouteRows(row: any): { key: string; host: string; showHost: boolean; path: string; svc: string; port: string }[] {
  return ingRoutes(row).map((r, i, all) => ({
    key: `${i}|${r.host}|${r.path}|${r.svc}`,
    host: r.host,
    showHost: i === 0 || all[i - 1].host !== r.host,
    path: r.path,
    svc: r.svc,
    port: r.port,
  }))
}
function ingRouteDetail(row: any): string {
  return ingRoutes(row).map((r) => `${r.host}  ${r.path} → ${r.svc}${r.port ? ':' + r.port : ''}`).join('\n')
}

// 上次退出详情（exit code / 原因 / 消息，来自 lastState.terminated）
const exitDlg = ref(false)
const exitRow = ref<any>(null)

// Describe（后端拼好的 kubectl describe 风格纯文本）
const describeDlg = ref(false)
const describeRow = ref<any>(null)
const describeText = ref('')
const describeLoading = ref(false)

async function openDescribe(row: any) {
  describeRow.value = row
  describeText.value = ''
  describeDlg.value = true
  describeLoading.value = true
  try {
    const res: any = await describePod(cluster.value, row.metadata.namespace, row.metadata.name)
    describeText.value = typeof res.data === 'string' ? res.data : String(res.data || '')
  } catch { /* http 层已提示 */ } finally { describeLoading.value = false }
}

async function copyDescribe() {
  try {
    await navigator.clipboard.writeText(describeText.value)
    ElMessage.success('已复制')
  } catch { ElMessage.warning('复制失败，请手动选择复制') }
}

function openExitInfo(row: any) {
  exitRow.value = row
  exitDlg.value = true
}

function fmtTs(v?: string): string {
  if (!v) return '-'
  const d = new Date(v)
  return isNaN(d.getTime()) ? v : d.toLocaleString('zh-CN', { hour12: false })
}

const exitInfo = computed(() => {
  const list = exitRow.value?.status?.containerStatuses || []
  return list.map((c: any) => {
    const t = c.lastState?.terminated
    return {
      name: c.name,
      ready: !!c.ready,
      restarts: c.restartCount ?? 0,
      hasTerm: !!t,
      exitCode: t?.exitCode,
      reason: t?.reason || (t ? '-' : ''),
      message: t?.message || '',
      startedAt: t ? fmtTs(t.startedAt) : '',
      finishedAt: t ? fmtTs(t.finishedAt) : '',
    }
  })
})

// ---------- 列排序（字段为嵌套结构，用自定义 sort-method）----------
// IP 数值序比较（按点分段比大小，10.0.1.2 < 10.0.1.10；非 IP 形态回退字典序）
function ipCompare(a: string, b: string): number {
  const AS = a.split('.'), BS = b.split('.')
  if (AS.length === 4 && BS.length === 4 && AS.every(x => /^\d+$/.test(x)) && BS.every(x => /^\d+$/.test(x))) {
    for (let i = 0; i < 4; i++) {
      const d = Number(AS[i]) - Number(BS[i])
      if (d) return d
    }
    return 0
  }
  return a.localeCompare(b)
}
function sortName(a: any, b: any): number {
  // nodes 主显示名为 IP，按 IP 数值序；其余资源按 metadata.name
  if (cur.resource === 'nodes') return ipCompare(nodeIp(a) || String(a.metadata?.name || ''), nodeIp(b) || String(b.metadata?.name || ''))
  return String(a.metadata?.name || '').localeCompare(String(b.metadata?.name || ''))
}

function sortNS(a: any, b: any): number {
  return String(a.metadata?.namespace || '').localeCompare(String(b.metadata?.namespace || ''))
}

function sortTime(a: any, b: any): number {
  return new Date(a.metadata?.creationTimestamp || 0).getTime() - new Date(b.metadata?.creationTimestamp || 0).getTime()
}

// 关键信息的排序值：按资源类型取主状态（Pod 阶段 / 就绪副本数 / Service 类型等）
function keyInfoOf(row: any): string | number {
  switch (cur.resource) {
    case 'pods': return podPhase(row)
    case 'deployments':
    case 'statefulsets':
    case 'daemonsets': return Number(row.status?.readyReplicas ?? row.status?.numberReady ?? 0)
    case 'jobs': return Number(row.status?.ready ?? 0)
    case 'cronjobs': return row.spec?.schedule ?? ''
    case 'services': return row.spec?.type || 'ClusterIP'
    case 'namespaces':
    case 'persistentvolumeclaims':
    case 'persistentvolumes': return row.status?.phase || ''
    case 'nodes': return nodeReady(row) === 'success' ? 1 : 0
    case 'ingresses': return ingHosts(row)[0] || '~' // 按首个 host 排序，无 host 的排最后
    case 'configmaps':
    case 'secrets': return Object.keys(row.data || {}).length
    case 'horizontalpodautoscalers': return `${row.spec?.minReplicas ?? 1}-${row.spec?.maxReplicas ?? '-'}`
    case 'poddisruptionbudgets':
      if (row.spec?.minAvailable !== undefined) return `minAvailable=${row.spec.minAvailable}`
      if (row.spec?.maxUnavailable !== undefined) return `maxUnavailable=${row.spec.maxUnavailable}`
      return '-'
    case 'networkpolicies': return (row.spec?.policyTypes || []).join('/') || '-'
    case 'endpointslices': return row.spec?.addressType || '-'
    case 'ingressclasses': return row.spec?.controller || '-'
    case 'resourcequotas': return Object.keys(row.spec?.hard || {}).length
    case 'limitranges': return row.spec?.limits?.length || 0
    case 'roles':
    case 'clusterroles': return row.rules?.length || 0
    case 'rolebindings':
    case 'clusterrolebindings': return `${row.roleRef?.kind || '-'}/${row.roleRef?.name || '-'}`
    default: return row.kind || ''
  }
}

function sortKeyInfo(a: any, b: any): number {
  const x = keyInfoOf(a)
  const y = keyInfoOf(b)
  if (typeof x === 'number' && typeof y === 'number') return x - y
  return String(x).localeCompare(String(y))
}

// ---------- 启停（平台侧：注解保存原副本数）----------
const STOP_ANN = 'kubeyard.io/original-replicas'

function isStopped(row: any): boolean {
  return ['deployments', 'statefulsets'].includes(cur.resource)
    && row.spec?.replicas === 0 && !!row.metadata?.annotations?.[STOP_ANN]
}

function workloadTarget(row: any) {
  return {
    group: cur.group, version: cur.version, resource: cur.resource,
    namespace: row.metadata.namespace || '', name: row.metadata.name,
  }
}

async function doStop(row: any) {
  try {
    await ElMessageBox.confirm(
      `确定停止 "${row.metadata.name}" 吗？副本将缩为 0，点「启动」时恢复为当前副本数（${row.spec?.replicas ?? 1}）。`,
      '停止确认', { type: 'warning' },
    )
  } catch { return }
  try {
    await stopResource(cluster.value, workloadTarget(row))
    ElMessage.success('已停止')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

async function doStart(row: any) {
  try {
    await ElMessageBox.confirm(`确定启动 "${row.metadata.name}" 吗？`, '启动确认', { type: 'info' })
  } catch { return }
  try {
    await startResource(cluster.value, workloadTarget(row))
    ElMessage.success('已启动')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

function onNavSelect(index: string) {
  const [g, v, r] = index.split('|')
  cur.group = g
  cur.version = v
  cur.resource = r
  activeKey.value = index
  // 切到 cluster-scoped 资源时清空 namespace，避免带 ?namespace= 误调
  if (isNamespaced.value) {
    const saved = loadResourceSelection()
    const savedNamespace = saved.namespaces[cluster.value]
    if (Object.prototype.hasOwnProperty.call(saved.namespaces, cluster.value) &&
      (!savedNamespace || namespaces.value.some((n: any) => n.name === savedNamespace))) {
      namespace.value = savedNamespace
    } else {
      pickDefaultNamespace()
    }
  } else {
    namespace.value = ''
  }
  saveResourceSelection()
  rowDrafts.value = {} // 换资源类型：行暂存缓存作废（key 里含 resource，不重置会串数据）
  rowDraftsSeq++ // 在途的暂存请求一并失效
  load()
}

// 集群显示名：纳管时填了描述则显示「描述(名称)」，否则显示名称
function clusterLabel(c: any): string {
  const desc = (c.description || '').trim()
  return desc ? `${desc}(${c.name})` : c.name
}

// ---------- Pod 监控（Grafana）----------
// 当前集群配置的监控地址前缀（集群纳管页配置，以 & 结尾；只接受 http/https 防 javascript: 等）
const grafanaBase = computed(() => {
  const c = clusters.value.find((x) => x.name === cluster.value)
  const raw = (c?.grafana_url || '').trim()
  if (!raw) return ''
  // scheme 校验：必须是 http(s)；同时去掉末尾的 ? 或 #（避免 var-* 被丢进 fragment/query）
  if (!/^https?:\/\//i.test(raw)) return ''
  return raw
})

// 拼接 namespace 与 pod 打开对应监控面板
function openGrafana(row: any) {
  const base = grafanaBase.value
  if (!base) return
  // 自动补 ? 或 &：用户配置时可能漏了；避免 var-* 落到 query 之外的 fragment
  const hasQ = base.includes('?')
  const sep = /[?&#]$/.test(base) ? '' : (hasQ ? '&' : '?')
  const url = `${base}${sep}var-namespace=${encodeURIComponent(row.metadata.namespace)}&var-pod=${encodeURIComponent(row.metadata.name)}`
  window.open(url, '_blank', 'noopener')
}

async function loadClusters() {
  clusters.value = (await listClusters()).data
  if (!cluster.value && clusters.value.length) {
    // 深链指定的集群优先，其次恢复该用户上次选择，最后回退到首个可用集群
    const qc = typeof route.query.cluster === 'string' ? route.query.cluster : ''
    const saved = loadResourceSelection()
    cluster.value = clusters.value.some((c: any) => c.name === qc) ? qc
      : clusters.value.some((c: any) => c.name === saved.cluster) ? saved.cluster
        : clusters.value[0].name
    const loaded = await loadNamespaces()
    canPersistResourceSelection = loaded
    const savedNamespace = saved.namespaces[cluster.value]
    const queryNamespace = typeof route.query.ns === 'string' ? route.query.ns : ''
    const preferredNamespace = queryNamespace || savedNamespace
    if (loaded && preferredNamespace !== undefined &&
      (!preferredNamespace || namespaces.value.some((n: any) => n.name === preferredNamespace))) {
      namespace.value = preferredNamespace
    } else {
      pickDefaultNamespace()
    }
    await loadCRs(cluster.value) // 首次进入：发现当前集群的 CR
    restoreResourceSelection()
  }
}

async function loadNamespaces() {
  if (!cluster.value) return false
  try {
    namespaces.value = (await listNamespaces(cluster.value)).data
    return true
  } catch {
    namespaces.value = []
    return false
  }
}

// 默认命名空间：取登录用户有权限的命名空间按名称排序后的第一个（避免默认全命名空间加载过慢；可手动清空选择查看全部）
function pickDefaultNamespace() {
  if (namespace.value) return
  const list = namespaces.value.map((n: any) => n.name).sort((a: string, b: string) => a.localeCompare(b))
  namespace.value = list[0] || ''
}

async function onClusterChange() {
  listSeq++ // 立即使在途列表请求失效：下方 loadNamespaces/loadCRs 的 await 间隙里，
  // 旧集群的响应返回仍会通过 seq 检查渲染到新集群页面上（外部评审核实）
  namespace.value = ''
  rowDrafts.value = {} // 换集群：行暂存缓存作废
  rowDraftsSeq++ // 在途的暂存请求一并失效（key 不含集群，旧响应串进来会跨集群误写）
  const loaded = await loadNamespaces()
  canPersistResourceSelection = loaded
  const saved = loadResourceSelection()
  const savedNamespace = saved.namespaces[cluster.value]
  if (loaded && savedNamespace !== undefined &&
    (!savedNamespace || namespaces.value.some((n: any) => n.name === savedNamespace))) {
    namespace.value = savedNamespace
  } else {
    pickDefaultNamespace()
  }
  await loadCRs(cluster.value) // 切换集群时重新发现 CR
  if (!restoreResourceSelection()) ensureSelection()
  saveResourceSelection(true)
  load()
}

function onNamespaceChange() {
  saveResourceSelection(true)
  load()
}

let searchTimer: number | null = null
watch(search, () => {
  listSeq++ // 输入即失效：300ms 防抖期间旧搜索词的响应不得渲染到新筛选条件下
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = window.setTimeout(load, 300)
})

// ---------- 自动刷新（30s；静默刷新不打 loading、不清勾选；页面卸载即清理）----------
const autoRefresh = ref(false)
let refreshTimer: number | null = null
// 定时任务对话框打开期间暂停自动刷新：刷新会替换表格数据触发整页重渲染，
// 正在输入的任务名/执行时间输入框光标会被重置
const scheduleDlgOpen = ref(false)
watch(autoRefresh, (on) => {
  if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null }
  if (on) refreshTimer = window.setInterval(() => { if (!scheduleDlgOpen.value) load(true) }, 30000)
})
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  if (searchTimer) clearTimeout(searchTimer)
})

// 请求序号防竞态（外部评审核实：快速切换集群/筛选后，前一个较晚返回的旧结果
// 会覆盖当前列表——用户可能据此对错误资源执行操作）
let listSeq = 0

// silent=true 静默刷新（自动刷新用）：不转 loading、保留表格勾选
async function load(silent = false) {
  if (!cluster.value) return
  const seq = ++listSeq
  const reqCluster = cluster.value
  const reqQuery = { group: cur.group, version: cur.version, resource: cur.resource,
    namespace: namespace.value || undefined, search: search.value || undefined }
  if (!silent) loading.value = true
  loadRowDrafts() // 列表「应用修改」按钮数据（异步，不阻塞列表）
  try {
    const res = await listResources(reqCluster, reqQuery)
    if (seq !== listSeq) return // 期间又发起了新请求：本次结果按过期丢弃
    items.value = res.data.items || []
    truncated.value = !!res.data.truncated
    // 深链直达详情：目标应用在本批数据里（搜索词已保证列表只含它）就开抽屉；没有说明无权限或已消失，静默放弃
    if (detailDeepLink.value) {
      const target = items.value.find((it: any) => it.metadata?.name === detailDeepLink.value)
      detailDeepLink.value = ''
      if (target) openDetail(target)
    }
  } catch { /* http 层已提示 */ } finally {
    if (seq === listSeq && !silent) {
      loading.value = false
      selectedRows.value = []
      tableRef.value?.clearSelection?.()
    }
  }
}

// ---------- 批量操作（多选）----------
const tableRef = ref()
const selectedRows = ref<any[]>([])
// Pod 状态呼吸灯分类：Running 绿色慢速呼吸；故障态红色快速涟漪（CrashLoop/Error/Failed/
// 驱逐/OOM/拉镜像失败等）；Succeeded 与其余不亮灯
const POD_BAD_STATES = ['CrashLoopBackOff', 'Error', 'Failed', 'Evicted', 'OOMKilled',
  'ImagePullBackOff', 'ErrImagePull', 'CreateContainerConfigError', 'CreateContainerError',
  'RunContainerError', 'InvalidImageName', 'DeadlineExceeded']
function podPulseClass(row: any): string {
  const phase = podPhase(row)
  if (phase === 'Running') return 'pulse-ok'
  if (POD_BAD_STATES.includes(phase)) return 'pulse-bad'
  return ''
}

// 容器状态点（关键信息列）：一容器一枚——
//   绿（呼吸）= Running 且 ready；红（涟漪）= waiting 非 PodInitializing / terminated 非零码（异常）；
//   灰（静态）= 已退出（terminated 正常收尾，如 Job 容器跑完）或 init 容器 / PodInitializing；
// 容器状态尚未上报（Pending 起 status.containerStatuses 为空）返回空数组，回退整 Pod 呼吸灯。
function cdotList(row: any): { key: string; cls: string; tip: string }[] {
  const cs: any[] = row.status?.containerStatuses || []
  if (!cs.length) return []
  const dots = cs.map((c, i) => {
    const state = c.state || {}
    const wait = state.waiting?.reason || ''
    const term = state.terminated
    const bad = (!!wait && wait !== 'PodInitializing') || (!!term && term.exitCode !== 0)
    const dim = (!!term && term.exitCode === 0) || wait === 'PodInitializing'
    const cls = bad ? 'cdot-bad' : (dim ? 'cdot-dim' : (c.ready ? 'cdot-ok' : 'cdot-dim'))
    // 悬浮提示带容器名，多容器时一眼对得上号
    const cur = bad ? wait || `退出码 ${term?.exitCode}` : (c.ready ? 'Running' : wait || '启动中')
    return { key: c.name || String(i), cls, tip: `容器 ${c.name}：${cur}` }
  })
  // initContainers 有状态记录时附在后面（通常一闪而过；PodInitializing 阶段可见灰点）
  const initCs: any[] = row.status?.initContainerStatuses || []
  for (const c of initCs) {
    const term = c.state?.terminated
    const bad = !!term && term.exitCode !== 0
    const running = !!c.state?.running
    dots.push({
      key: 'init:' + c.name,
      cls: bad ? 'cdot-bad' : (running || term ? 'cdot-dim' : 'cdot-dim'),
      tip: `init 容器 ${c.name}：${bad ? `退出码 ${term?.exitCode}` : (running ? '运行中' : term ? '已完成' : '等待中')}`,
    })
  }
  return dots
}

const supportsBatch = computed(() => ['deployments', 'statefulsets', 'daemonsets', 'pods'].includes(cur.resource))
const canBatchStop = computed(() => ['deployments', 'statefulsets'].includes(cur.resource) && actionAllowed(authResKey.value, 'stop', namespace.value))

function onSelectionChange(rows: any[]) {
  selectedRows.value = rows
}

function nameList(rows: any[]): string {
  const names = rows.map((r) => r.metadata.name)
  return names.slice(0, 8).join('、') + (names.length > 8 ? ` …等共 ${names.length} 个` : '')
}

async function confirmBatch(verb: string, rows: any[], extra = ''): Promise<boolean> {
  try {
    await ElMessageBox.confirm(
      `确定${verb}以下 ${rows.length} 个应用${extra ? `（${extra}）` : ''}？\n${nameList(rows)}`,
      `${verb}确认`, { type: 'warning', confirmButtonText: verb, cancelButtonText: '取消' },
    )
    return true
  } catch { return false }
}

async function runBatch(verb: string, rows: any[], fn: (row: any) => Promise<any>) {
  const fail: string[] = []
  for (const row of rows) {
    try { await fn(row) } catch (e) { fail.push(`${row.metadata.name}：${errMsg(e)}`) }
  }
  if (fail.length) {
    ElMessageBox.alert(`成功 ${rows.length - fail.length} 个，失败 ${fail.length} 个：\n${fail.join('\n')}`,
      `${verb}结果`, { type: 'warning', confirmButtonText: '知道了' })
  } else {
    ElMessage.success(`${verb}完成（${rows.length} 个）`)
  }
  load()
}

async function batchStop() {
  const rows = selectedRows.value.filter((r) => !isStopped(r))
  const skipped = selectedRows.value.length - rows.length
  if (!rows.length) {
    ElMessage.warning('所选应用均已处于停止状态')
    return
  }
  const extra = `启动后恢复原副本数${skipped ? `；已跳过 ${skipped} 个处于停止状态的项` : ''}`
  if (!await confirmBatch('停止', rows, extra)) return
  await runBatch('停止', rows, (row) => stopResource(cluster.value, workloadTarget(row)))
}

async function batchRestart() {
  const rows = selectedRows.value
  if (!await confirmBatch('重启', rows, '滚动重启')) return
  await runBatch('重启', rows, (row) => restartResource(cluster.value, workloadTarget(row)))
}

async function batchDelete() {
  const rows = selectedRows.value
  if (cur.resource === 'pods') return batchDeletePods(rows)
  try {
    // 防误触：需输入「删除」二字确认
    await ElMessageBox.prompt(
      `将永久删除以下 ${rows.length} 个应用及其 Pod，不可恢复：\n${nameList(rows)}\n\n请输入「删除」二字确认：`,
      '批量删除确认', {
        type: 'error', confirmButtonText: '删除', cancelButtonText: '取消',
        inputValidator: (v: string) => v.trim() === '删除' || '请输入「删除」以确认',
      },
    )
  } catch { return }
  await runBatch('删除', rows, (row) => deleteResource(cluster.value, workloadTarget(row)))
}

// Pod 批量删除：双确认（列明细 + 确认按钮）。
// Pod 删除语义与 workload 不同——受控制器管理的 Pod 删后按副本数重建（滚动重建），
// 裸 Pod 删除后真没了，确认文案如实区分；逐 Pod 走既有删除通道（授权逐条校验）
async function batchDeletePods(rows: any[]) {
  const bare = rows.filter((r) => !(r.metadata?.ownerReferences || []).some((o: any) => o.controller))
  const desc = bare.length
    ? `其中 ${bare.length} 个是裸 Pod（无控制器），删除后不会重建；其余受控制器管理的会按副本数重建。此操作不可恢复。`
    : '受控制器管理的 Pod 删除后会按副本数重建（滚动重建）。此操作不可恢复。'
  try {
    await ElMessageBox.confirm(
      `确定删除以下 ${rows.length} 个 Pod？
${nameList(rows)}

${desc}`,
      '批量删除 Pod', { type: 'error', confirmButtonText: '确认删除', cancelButtonText: '取消' },
    )
  } catch { return }
  await runBatch('删除', rows, (row) => deleteResource(cluster.value, workloadTarget(row)))
}

function envToRow(e: any) {
  if (e.valueFrom?.secretKeyRef) return { name: e.name, type: 'secret', value: '', refName: e.valueFrom.secretKeyRef.name || '', refKey: e.valueFrom.secretKeyRef.key || '' }
  if (e.valueFrom?.configMapKeyRef) return { name: e.name, type: 'configmap', value: '', refName: e.valueFrom.configMapKeyRef.name || '', refKey: e.valueFrom.configMapKeyRef.key || '' }
  if (e.valueFrom?.fieldRef) return { name: e.name, type: 'fieldref', value: '', refName: '', refKey: e.valueFrom.fieldRef.fieldPath || '' }
  return { name: e.name || '', type: 'value', value: e.value ?? '', refName: '', refKey: '' }
}

// ---------- 暂存 / 立即更新（表单编辑草稿：只存平台不写集群，应用时走 resedit 通道）----------
// 会改动 Pod 模板的模块：立即更新触发滚动重启，需确认；labels/annotations/副本数/Service 端口不重启
// （Drafts.vue 管理页有同名副本，两处需同步维护）
const restartSections = ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage', 'scheduling']
// CM/Secret 的 data 保存不重启 Pod，但存量 Pod 不会自动感知，改完要重启应用才生效——单独文案
const cfgDataRestartHint = '已运行的 Pod 不会自动加载新配置，需重启应用后生效'
const staging = ref(false)
const applying = ref(false)

const draftState = reactive<{ loaded: boolean; map: Record<string, any> }>({ loaded: false, map: {} })
const draftKey = (ns: string, res: string, name: string, section: string) => `${ns}|${res}|${name}|${section}`
const draftOfCurrent = computed(() => draftState.map[draftKey(
  detailTarget.value?.metadata?.namespace || '', cur.resource, detailTarget.value?.metadata?.name || '', formTab.value,
)] || null)
// 某 section 是否有暂存（tab 角标用：哪个模块有待应用的变更就标哪个，与是否激活无关）
function draftFor(section: string): any {
  return draftState.map[draftKey(
    detailTarget.value?.metadata?.namespace || '', cur.resource, detailTarget.value?.metadata?.name || '', section,
  )] || null
}

// ---------- 列表操作栏「应用修改」（当前资源类型有暂存的行显示）----------
const rowDrafts = ref<Record<string, any[]>>({}) // "ns|resource|name" → 暂存数组（load 时刷新）
const applyingRows = ref<Set<string>>(new Set())
const rowDraftKey = (ns: string, name: string) => `${ns || ''}|${cur.resource}|${name}`
const rowDraftsOf = (row: any): any[] => rowDrafts.value[rowDraftKey(row.metadata.namespace, row.metadata.name)] || []
const applyingRow = (row: any) => applyingRows.value.has(rowDraftKey(row.metadata.namespace, row.metadata.name))

// 行级应用确认与执行（重启提示集中在确认弹窗）
async function applyRowDrafts(row: any) {
  const drafts = rowDraftsOf(row)
  if (!drafts.length) return
  const ns = row.metadata.namespace || ''
  const podTpl = drafts.some((d) => restartSections.includes(d.section))
  const cfgData = drafts.some((d) => (d.resource === 'configmaps' || d.resource === 'secrets') && d.section === 'data')
  const mods = drafts.map((d) => d.section_label || d.section).join('、')
  let msg = `将应用 ${drafts.length} 个模块的暂存修改（${mods}）写入集群。`
  if (podTpl) msg += '\n该应用包含 Pod 模板类修改，应用后容器将滚动重启（Pod 逐个重建，滚动策略下业务不中断）。'
  if (cfgData) msg += `\n${cfgDataRestartHint}。`
  try {
    await ElMessageBox.confirm(msg, `应用修改 - ${row.metadata.name}`,
      { type: 'warning', confirmButtonText: '确认应用', cancelButtonText: '取消' })
  } catch { return }
  const key = rowDraftKey(ns, row.metadata.name)
  applyingRows.value.add(key)
  let ok = 0
  try {
    for (const d of drafts) {
      try {
        await resourceFormEdit(cluster.value, {
          group: d.group || '', version: d.version || '', resource: d.resource,
          namespace: ns, name: row.metadata.name,
        }, d.section, d.payload)
        await draftDelete(cluster.value, { resource: d.resource, namespace: ns, name: row.metadata.name, section: d.section })
        ok++
      } catch (e) {
        ElMessage.error(`「${d.section_label || d.section}」应用失败：${errMsg(e)}`)
      }
    }
    if (ok) ElMessage.success(`已应用 ${ok} 个模块的暂存修改`)
    await loadRowDrafts()
    load()
  } finally { applyingRows.value.delete(key); applyingRows.value = new Set(applyingRows.value) }
}

// 拉当前集群+资源类型的暂存（load 与切换资源时调用；403 等失败静默——按钮不显示即可）。
// 代次+上下文校验（外部评审核实）：rowDraftKey 不含集群，切换集群/资源后慢返回的
// 旧响应若覆盖新上下文的暂存数据，A 集群的暂存会串到 B 集群同名行——点「应用修改」
// 即用 A 的 payload 写 B 的资源（跨集群误写）
let rowDraftsSeq = 0
async function loadRowDrafts() {
  if (!cluster.value || !simpleNavRes()) return
  const my = ++rowDraftsSeq
  const reqCluster = cluster.value
  const reqResource = cur.resource
  try {
    const res = await draftList(reqCluster, { resource: reqResource })
    if (my !== rowDraftsSeq || cluster.value !== reqCluster || cur.resource !== reqResource) return
    const m: Record<string, any[]> = {}
    for (const d of res.data || []) {
      const k = `${d.namespace || ''}|${d.resource}|${d.name}`
      ;(m[k] = m[k] || []).push(d)
    }
    rowDrafts.value = m
  } catch { if (my === rowDraftsSeq) rowDrafts.value = {} }
}

// 导航资源是否为平台管理的内置资源（draft 端点只覆盖内置；CR 无暂存入口）
function simpleNavRes(): boolean {
  return !isCR(cur.resource) && !['pods', 'nodes', 'namespaces', 'endpoints'].includes(cur.resource)
}

// 暂存时间显示（本地时区到分钟）
function fmtDraftTime(s?: string): string {
  if (!s) return ''
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? s : `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

// 详情对象的暂存草稿。代次+上下文校验（外部评审核实）：draftKey 不含集群，详情
// 切到其他对象/集群后慢返回的旧响应若写入 map，B 集群同名对象会显示 A 集群的暂存
// 提示，「直接应用」即写错集群。加载前先清残留：本次加载失败（403/网络）时，
// 上一次对象的 map 不得继续命中
let draftsSeq = 0
async function loadDrafts() {
  if (!cluster.value || !detailTarget.value) return
  const my = ++draftsSeq
  const reqCluster = cluster.value
  const reqResource = cur.resource
  const reqNS = detailTarget.value.metadata.namespace || ''
  const reqName = detailTarget.value.metadata.name
  draftState.map = {}
  draftState.loaded = false
  try {
    const res = await draftList(reqCluster, {
      namespace: reqNS,
      resource: reqResource, name: reqName,
    })
    if (my !== draftsSeq || cluster.value !== reqCluster || cur.resource !== reqResource
      || !detailTarget.value || (detailTarget.value.metadata.namespace || '') !== reqNS
      || detailTarget.value.metadata.name !== reqName) return // 期间详情已切换：旧响应丢弃
    const m: Record<string, any> = {}
    for (const d of res.data || []) m[draftKey(d.namespace, d.resource, d.name, d.section)] = d
    draftState.map = m
    draftState.loaded = true
  } catch { if (my === draftsSeq) draftState.loaded = false }
}

// 暂存：表单内容与保存同校验，payload 同构存平台（不写集群）
async function stageFormSection() {
  const row = detailTarget.value
  if (!row) return
  const section = formTab.value
  const verr = validateFormSection(section)
  if (verr) { ElMessage.warning(verr); return }
  // 复用保存的 payload 构造：临时切 section 调 saveFormSection 的构造逻辑太绕，直接内联构造
  const payload = buildSectionPayload(section)
  if (payload === null) return
  staging.value = true
  try {
    await draftStage(cluster.value, {
      resource: cur.resource, group: cur.group, version: cur.version,
      namespace: row.metadata.namespace || '', name: row.metadata.name,
      section, section_label: currentSectionLabel.value,
      resource_version: String(detailObj.value?.metadata?.resourceVersion || ''),
      payload,
    })
    ElMessage.success('已暂存（未修改集群，不会触发重启；可在本模块、列表操作栏或暂存列表应用）')
    await loadDrafts()
    loadRowDrafts() // 列表操作栏「应用修改」按钮同步出现
  } catch (e) { ElMessage.error(errMsg(e)) } finally { staging.value = false }
}

// 立即更新：pod 模板模块二次确认（滚动重启提示），然后走保存
async function applyNow() {
  const section = formTab.value
  if (restartSections.includes(section)) {
    try {
      await ElMessageBox.confirm(
        `立即更新「${currentSectionLabel.value}」将修改应用配置并触发滚动重启（Pod 逐个重建，滚动策略下业务不中断）。`,
        '确认立即更新', { type: 'warning', confirmButtonText: '仍要立即更新', cancelButtonText: '改为暂存' },
      )
    } catch { return }
  } else if ((cur.resource === 'configmaps' || cur.resource === 'secrets') && section === 'data') {
    try {
      await ElMessageBox.confirm(
        `保存后${cfgDataRestartHint}。是否立即更新？`,
        '确认立即更新', { type: 'info', confirmButtonText: '仍要立即更新', cancelButtonText: '取消' },
      )
    } catch { return }
  }
  saveFormSection()
}

// 应用暂存（banner 按钮 / 暂存列表页共用）：漂移提示 → 确认 → resedit → 删草稿
async function applyDraft(d: any, opts?: { silent?: boolean; reloadAfter?: boolean }) {
  const ns = d.namespace || ''
  // 漂移检测：暂存后对象被别人改过则提醒（不硬拦；存储挂载模块后端有 keepVolumes 深度校验兜底）
  if (!opts?.silent) {
    try {
      const cur0 = await getResource(cluster.value, {
        group: d.group, version: d.version, resource: d.resource, namespace: ns, name: d.name,
      })
      const curRV = String(cur0.data?.metadata?.resourceVersion || '')
      if (d.resource_version && curRV && d.resource_version !== curRV) {
        await ElMessageBox.confirm(
          '暂存后该对象已被其他人修改过，直接应用会以暂存内容覆盖当前配置。建议先「载入表单」确认差异。',
          '对象已变更', { type: 'warning', confirmButtonText: '仍要应用', cancelButtonText: '取消' },
        )
      }
    } catch (e: any) {
      if (e !== 'cancel' && e?.message !== 'cancel') return // 请求失败直接中止
    }
  }
  const isPodTpl = restartSections.includes(d.section)
  const hint = isPodTpl
    ? '应用将修改应用配置并触发滚动重启（Pod 逐个重建，滚动策略下业务不中断）。'
    : (d.resource === 'configmaps' || d.resource === 'secrets') && d.section === 'data'
      ? `保存后${cfgDataRestartHint}。`
      : ''
  try {
    if (hint && !opts?.silent) { // silent（批量应用）时重启提示由外层统一确认
      await ElMessageBox.confirm(hint, '确认应用暂存', { type: 'warning', confirmButtonText: '应用', cancelButtonText: '取消' })
    }
  } catch { return }
  const target = { group: d.group || '', version: d.version || '', resource: d.resource, namespace: ns, name: d.name }
  applying.value = true
  try {
    await resourceFormEdit(cluster.value, target, d.section, d.payload)
    await draftDelete(cluster.value, { resource: d.resource, namespace: ns, name: d.name, section: d.section })
    if (!opts?.silent) ElMessage.success(`已应用暂存${hint ? '（' + hint.replace(/。$/, '') + '）' : ''}`)
    delete draftState.map[draftKey(ns, d.resource, d.name, d.section)]
    loadRowDrafts() // 列表操作栏「应用修改」按钮同步消失
    if (opts?.reloadAfter) { await refreshDetail(); load() }
    return true
  } catch (e) {
    if (!opts?.silent) ElMessage.error(errMsg(e))
    return false
  } finally { applying.value = false }
}

// 放弃暂存
async function discardDraft(d: any, opts?: { reloadAfter?: boolean }) {
  try {
    await ElMessageBox.confirm(`确定放弃「${d.section_label || d.section}」的暂存修改？放弃后不可恢复。`, '放弃暂存',
      { type: 'warning', confirmButtonText: '放弃', cancelButtonText: '取消' })
  } catch { return }
  try {
    await draftDelete(cluster.value, { resource: d.resource, namespace: d.namespace || '', name: d.name, section: d.section })
    delete draftState.map[draftKey(d.namespace || '', d.resource, d.name, d.section)]
    ElMessage.success('已放弃暂存')
    loadRowDrafts() // 列表操作栏「应用修改」按钮同步消失
    if (opts?.reloadAfter) refreshDetail()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

// 载入表单：把暂存 payload 回填到编辑表单（按 section 分发，与 buildForm 的解析逻辑对齐）
// 暂存的 scheduling payload 行（values 数组）→ SchedEditor 行模型（values 逗号字符串）
function schedPayloadToRow(r: any): any {
  const row: any = {
    key: r?.key || '', operator: r?.operator || 'In',
    values: Array.isArray(r?.values) ? r.values.join(',') : (r?.values || ''),
  }
  if (r?.weight != null) row.weight = r.weight
  if (r?.topology_key !== undefined) row.topology_key = r.topology_key || ''
  return row
}

function loadDraftToForm(d: any) {
  const p = d.payload || {}
  if (d.section === 'labels' || d.section === 'annotations') {
    const rows: any[] = []
    for (const [k, v] of Object.entries(p[d.section] || {})) rows.push({ key: k, value: String(v) })
    metaForm[d.section as 'labels' | 'annotations'] = rows
  } else if ((cur.resource === 'configmaps' || cur.resource === 'secrets') && d.section === 'data') {
    const isSecret = cur.resource === 'secrets'
    const rows: any[] = []
    for (const [k, v] of Object.entries(p.data || {})) {
      let value = String(v ?? ''); let binary = false
      if (isSecret) { try { value = decodeURIComponent(escape(atob(value))) } catch { binary = true } }
      rows.push({ key: k, value, binary })
    }
    dataForm.rows = rows
  } else if (cur.resource === 'persistentvolumeclaims' && d.section === 'storage') {
    pvcCapForm.size = String(p.storage || '')
  } else if (d.section === 'taints') {
    taintForm.rows = (p.taints || []).map((t: any) => ({ key: t.key || '', value: t.value || '', effect: t.effect || 'NoSchedule' }))
  } else if (d.section === 'scheduling') {
    // 暂存 payload 与后端同形（values 已是数组），回填拆成逗号字符串
    schedForm.value = {
      node_name: p.node_name || '',
      node_selector: Object.entries(p.node_selector || {}).map(([key, value]) => ({ key, value: String(value) })),
      node_affinity_required: (p.node_affinity_required || []).map(schedPayloadToRow),
      node_affinity_preferred: (p.node_affinity_preferred || []).map(schedPayloadToRow),
      pod_anti_required: (p.pod_anti_required || []).map(schedPayloadToRow),
      pod_anti_preferred: (p.pod_anti_preferred || []).map(schedPayloadToRow),
    }
    schedUnmodeledFlag.value = false
  } else if (cur.resource === 'ingresses' && d.section === 'routes') {
    ingForm.rows = (p.routes || []).map((x: any) => ({
      host: x.host || '', path: x.path || '/', pathType: x.pathType || 'Prefix', svc: x.svc || '', port: x.port ?? '',
    }))
  } else if (cur.resource === 'services') {
    if (d.section === 'ports') {
      svcForm.ports = (p.ports || []).map((x: any) => ({
        name: x.name || '', port: x.port ?? null, targetPort: x.targetPort ?? '', protocol: x.protocol || 'TCP', nodePort: x.nodePort ?? null,
      }))
    } else {
      svcForm.selector = Object.entries(p.selector || {}).map(([k, v]) => ({ key: k, value: String(v) }))
    }
  } else {
    // 工作负载模块：payload.containers 与 buildForm 产出的 formModel 对齐
    if (d.section === 'storage') {
      // 存储挂载：payload 是最终形态（卷描述 + 保留卷），回填按挂载行展开
      const descByName: Record<string, any> = {}
      for (const v of p.volumes || []) descByName[v.name] = v
      const typeOf = (v: any) => v.persistentVolumeClaim ? 'pvc' : v.configMap ? 'configmap' : v.secret ? 'secret' : v.emptyDir ? 'emptydir' : ''
      const refOf = (v: any) => v.persistentVolumeClaim?.claimName || v.configMap?.name || v.secret?.secretName || ''
      const mounted = new Set((p.containers || []).flatMap((c: any) => (c.mounts || []).map((m: any) => m.name)))
      const orphans = (p.volumes || []).filter((v: any) => !mounted.has(v.name) && typeOf(v))
      for (const ct of formModel.value) {
        const edit = (p.containers || []).find((c: any) => c.name === ct.name)
        ct.mounts = (edit?.mounts || []).map((m: any) => {
          const desc = descByName[m.name] || {}
          return {
            type: typeOf(desc) || 'pvc', refName: refOf(desc), mountPath: m.mountPath || '',
            subPath: m.subPath || '', readOnly: !!m.readOnly, volName: m.name,
          }
        })
        ct.keptMounts = edit?.keepMounts || []
      }
      // 暂存里存在但没有任何容器挂载的卷：按第一个容器追加回填（保留用户暂存的意图）
      for (const v of orphans) {
        const ct = formModel.value[0]
        if (!ct) break
        ct.mounts.push({ type: typeOf(v), refName: refOf(v), mountPath: '/（暂存：请补挂载路径）', subPath: '', readOnly: false, volName: v.name })
      }
    } else {
      for (const ct of formModel.value) {
        const edit = (p.containers || []).find((c: any) => c.name === ct.name)
        if (!edit) continue
        if (d.section === 'image') ct.image = edit.image || ct.image
        if (d.section === 'command') { const cv = cmdFormVals(edit.command, edit.args); ct.command = cv.command; ct.args = cv.args }
        if (d.section === 'ports') ct.ports = (edit.ports || []).map((x: any) => ({ name: x.name || '', containerPort: x.containerPort ?? null, hostPort: x.hostPort ?? null, protocol: x.protocol || 'TCP' }))
        if (d.section === 'resources') {
          for (const [phase, r] of [['limits', edit.resources?.limits], ['requests', edit.resources?.requests]] as const) {
            if (r?.cpu) ct.resources[phase].cpu = parseQty(r.cpu, 'cpu')
            if (r?.memory) ct.resources[phase].memory = parseQty(r.memory, 'mem')
          }
          ct.resources.extended = extendedRows(edit.resources?.limits)
        }
        if (d.section === 'env') ct.env = (edit.env || []).map(envToRow)
        if (d.section === 'probes') {
          // payload.probes = [{kind, probe}]；probe=null 删除，未列出的 kind 保持当前值
          for (const pe of edit.probes || []) {
            if (ct.probes?.[pe.kind] != null) ct.probes[pe.kind] = probeFromPayload(pe.probe)
          }
        }
      }
    }
  }
  ElMessage.success('已载入表单（可继续修改后暂存或立即更新）')
}

// 暂存构造 payload：与 saveFormSection 的分支逻辑一致（存储挂载含卷名分配与保留卷）
function buildSectionPayload(section: string): any {
  if (section === 'labels' || section === 'annotations') {
    return { [section]: metaRowsToObj(section) || {} }
  }
  if ((cur.resource === 'configmaps' || cur.resource === 'secrets') && section === 'data') {
    const isSecret = cur.resource === 'secrets'
    const data: Record<string, string> = {}
    for (const r of dataForm.rows) {
      const k = r.key.trim()
      if (!k) continue
      data[k] = isSecret && !r.binary ? btoa(unescape(encodeURIComponent(r.value))) : r.value
    }
    return { data }
  }
  if (cur.resource === 'persistentvolumeclaims' && section === 'storage') {
    return { storage: pvcCapForm.size.trim() }
  }
  if (cur.resource === 'ingresses' && section === 'routes') {
    return { routes: ingForm.rows.map((r) => ({
      host: (r.host || '').trim(), path: (r.path || '').trim(), pathType: r.pathType || 'Prefix',
      svc: (r.svc || '').trim(), port: String(r.port ?? '').trim(),
    })) }
  }
  if (cur.resource === 'services') {
    if (section === 'ports') {
      return {
        ports: svcForm.ports
          .filter((p) => p.port !== null && p.port !== undefined && p.port !== '')
          .map((p) => ({
            name: p.name || undefined, port: portVal(p.port),
            targetPort: portVal(p.targetPort) ?? portVal(p.port), protocol: p.protocol || 'TCP',
            nodePort: portVal(p.nodePort),
          })),
      }
    }
    return { selector: Object.fromEntries(svcForm.selector.filter((s) => s.key.trim()).map((s) => [s.key.trim(), s.value])) }
  }
  if (section === 'taints') {
    // 节点污点全量覆盖：空 value 省略（key+effect 合法）；空键行丢弃
    return {
      taints: taintForm.rows
        .filter((r) => r.key.trim())
        .map((r) => ({ key: r.key.trim(), ...(r.value.trim() ? { value: r.value.trim() } : {}), effect: r.effect || 'NoSchedule' })),
    }
  }
  if (section === 'scheduling') {
    // 后端 payload 与 SchedEditor 行模型同形，仅 values 需拆数组（UI 内是逗号分隔字符串）
    const splitVals = (s: string) => (s || '').split(',').map((v) => v.trim()).filter(Boolean)
    const exprRows = (rows: any[]) => rows.filter((r) => r.key.trim()).map((r) => {
      const out: any = { key: r.key.trim(), operator: r.operator || 'In', values: splitVals(r.values) }
      if (r.weight != null) out.weight = r.weight
      if (r.topology_key !== undefined) out.topology_key = (r.topology_key || '').trim()
      return out
    })
    const nodeSelector: Record<string, string> = {}
    for (const r of schedForm.value.node_selector) {
      if (r.key.trim()) nodeSelector[r.key.trim()] = r.value.trim()
    }
    return {
      node_selector: nodeSelector,
      node_name: (schedForm.value.node_name || '').trim(),
      node_affinity_required: exprRows(schedForm.value.node_affinity_required),
      node_affinity_preferred: exprRows(schedForm.value.node_affinity_preferred),
      pod_anti_required: exprRows(schedForm.value.pod_anti_required),
      pod_anti_preferred: exprRows(schedForm.value.pod_anti_preferred),
    }
  }
  if (section === 'storage') {
    const assigned = storageAssignVolNames()
    if (assigned.err) { ElMessage.warning(assigned.err); return null }
    const rowNames = new Set(assigned.descs.map((d: any) => d.name))
    const keepVolumes = volState.raw.filter((v: any) => !rowNames.has(v.name) && (!volState.modeled[v.name] || volState.initNames.has(v.name)))
    return {
      volumes: assigned.descs, keepVolumes,
      containers: formModel.value.map((ct: any) => ({
        name: ct.name,
        mounts: ct.mounts.map((m: any) => ({ name: m.volName, mountPath: m.mountPath, subPath: m.subPath || '', readOnly: !!m.readOnly })),
        keepMounts: ct.keptMounts || [],
      })),
    }
  }
  return {
    containers: formModel.value.map((ct) => {
      if (section === 'image') return { name: ct.name, image: ct.image.trim() }
      if (section === 'command') {
        const cp = cmdPartsFromForm(ct.command || '', ct.args || '', false)
        return { name: ct.name, ...cp }
      }
      if (section === 'ports') {
        return {
          name: ct.name,
          ports: ct.ports
            .filter((p) => p.containerPort !== null && p.containerPort !== undefined)
            .map((p) => ({
              name: p.name || undefined, containerPort: p.containerPort,
              ...(p.hostPort ? { hostPort: p.hostPort } : {}),
              protocol: p.protocol || 'TCP',
            })),
        }
      }
      if (section === 'resources') {
        const r: any = {}
        const lim = cleanRes(ct.resources.limits)
        const req = cleanRes(ct.resources.requests)
        for (const e of ct.resources.extended || []) {
          if (e.key?.trim() && e.value !== '' && e.value != null) lim[e.key.trim()] = String(e.value)
        }
        if (Object.keys(lim).length) r.limits = lim
        if (Object.keys(req).length) r.requests = req
        return { name: ct.name, resources: r }
      }
      if (section === 'probes') {
        // 每容器三类探针的完整意图：probe=null 删除；罕见类型（raw）不发该条 = 后端原样保留
        const probes = probeKeys
          .map((k) => ({ kind: k, probe: probeToPayload(ct.probes?.[k], k) }))
          .filter((x: any) => x.probe !== undefined)
        return { name: ct.name, probes }
      }
      return {
        name: ct.name,
        env: ct.env.filter((e) => e.name.trim()).map((e) => {
          if (e.type === 'secret') return { name: e.name.trim(), valueFrom: { secretKeyRef: { name: e.refName.trim(), key: e.refKey.trim() } } }
          if (e.type === 'configmap') return { name: e.name.trim(), valueFrom: { configMapKeyRef: { name: e.refName.trim(), key: e.refKey.trim() } } }
          if (e.type === 'fieldref') return { name: e.name.trim(), valueFrom: { fieldRef: { apiVersion: 'v1', fieldPath: e.refKey.trim() } } }
          return { name: e.name.trim(), value: e.value }
        }),
      }
    }),
  }
}

// 资源量解析："500m" → {num:"500", unit:"m"}；"1Gi" → {num:"1", unit:"Gi"}；"0.5" → 核；空 → 默认单位
function parseQty(v: any, kind: 'cpu' | 'mem'): { num: string; unit: string } {
  const def = kind === 'cpu' ? 'core' : 'Gi'
  const s = String(v ?? '').trim()
  if (!s) return { num: '', unit: def }
  const m = s.match(/^([\d.]+)(.*)$/)
  if (!m) return { num: s, unit: '' } // 非常规值：保存时原样传回
  const [, num, unit] = m
  if (kind === 'cpu') {
    if (unit === 'm') return { num, unit: 'm' }
    return { num, unit: 'core' } // 无后缀（如 0.5）= 核；n/u 等罕见后缀按核处理并提示
  }
  if (['Ki', 'Mi', 'Gi', 'Ti'].includes(unit)) return { num, unit }
  if (unit === '') return { num, unit: 'Mi' } // 纯数字（字节）按 Mi 展示数值由用户确认
  return { num: s, unit: '' }
}

// 组合回 k8s 数量字符串；unit 为空表示"原值"（num 里已是完整字符串）
function qtyStr(q: any): string {
  const num = String(q?.num ?? '').trim()
  if (!num) return ''
  if (!q.unit || q.unit === 'core') return num // 核 = 纯数字
  return `${num}${q.unit}`
}

// 资源量字符串 → 统一单位的数值（cpu: 毫核；mem: Mi），用于请求 vs 上限比较；不合法返回 null
function qtyNum(s: string, kind: 'cpu' | 'mem'): number | null {
  const m = String(s || '').trim().match(/^([\d.]+)(m|Ki|Mi|Gi|Ti)?$/)
  if (!m) return null
  const n = parseFloat(m[1])
  if (Number.isNaN(n)) return null
  if (kind === 'cpu') return m[2] === 'm' ? n : n * 1000
  const mult: Record<string, number> = { Ki: 1 / 1024, Mi: 1, Gi: 1024, Ti: 1024 * 1024 }
  return n * (m[2] ? mult[m[2]] : 1 / 1024 / 1024) // 无单位 = 字节
}

function resourceBoundsError(limits: any, requests: any): string | null {
  const quantities = [limits.cpu, requests.cpu, limits.memory, requests.memory]
  if (quantities.some((q: any) => q?.num !== '' && q?.num != null && !/^\d+(\.\d+)?$/.test(String(q.num)))) {
    return '资源数值需为数字（如 0.5 / 2）'
  }
  const limitCpu = qtyStr(limits.cpu), requestCpu = qtyStr(requests.cpu)
  const limitMemory = qtyStr(limits.memory), requestMemory = qtyStr(requests.memory)
  const limitCpuValue = qtyNum(limitCpu, 'cpu'), requestCpuValue = qtyNum(requestCpu, 'cpu')
  const limitMemoryValue = qtyNum(limitMemory, 'mem'), requestMemoryValue = qtyNum(requestMemory, 'mem')
  if (limitCpuValue != null && requestCpuValue != null && requestCpuValue > limitCpuValue) {
    return `CPU 请求（${requestCpu}）大于上限（${limitCpu}）`
  }
  if (limitMemoryValue != null && requestMemoryValue != null && requestMemoryValue > limitMemoryValue) {
    return `内存请求（${requestMemory}）大于上限（${limitMemory}）`
  }
  return null
}

function fdResourceBoundsError(ct: any): string | null {
  return resourceBoundsError(
    { cpu: ct.res.limitsCpu, memory: ct.res.limitsMem },
    { cpu: ct.res.reqCpu, memory: ct.res.reqMem },
  )
}

function formResourceBoundsError(ct: any): string | null {
  return resourceBoundsError(ct.resources.limits, ct.resources.requests)
}

function qtyObj(v: any, kind: 'cpu' | 'mem') {
  return parseQty(v, kind)
}

// ---------- 健康检查（探针）模块 ----------
// 三类探针：存活（失败重启容器）/ 就绪（失败摘流量不重启）/ 启动（慢启动应用，成功前不跑其他探针）
const probeKeys = ['liveness', 'readiness', 'startup'] as const
const probeMeta: Record<string, { label: string; desc: string }> = {
  liveness: { label: '存活检查', desc: '失败达到阈值后容器被重启（自愈），适合会死锁但进程不退出的应用' },
  readiness: { label: '就绪检查', desc: '失败后实例从 Service 摘除流量、恢复后自动加回，不重启容器' },
  startup: { label: '启动检查', desc: '慢启动应用（如 JVM）用它顶住启动期，成功前不执行存活/就绪检查' },
}
function newProbeRow() {
  return { type: 'none', path: '/', port: '', scheme: 'HTTP', command: '', initialDelay: 0, period: 10, timeout: 1, success: 1, failure: 3, raw: null as any }
}
// k8s 探针对象 → 表单行；http/tcp/exec 可建模，其余罕见类型（grpc 等）记 raw 保存时原样保留
function probeFromK8s(p: any) {
  const row = newProbeRow()
  if (!p || typeof p !== 'object') return row
  if (p.httpGet) {
    row.type = 'http'
    row.path = p.httpGet.path || '/'
    row.port = p.httpGet.port ?? ''
    row.scheme = p.httpGet.scheme === 'HTTPS' ? 'HTTPS' : 'HTTP'
  } else if (p.tcpSocket) {
    row.type = 'tcp'
    row.port = p.tcpSocket.port ?? ''
  } else if (p.exec) {
    row.type = 'exec'
    row.command = (p.exec.command || []).map((s: string) => /\s/.test(s) ? `"${s}"` : s).join(' ')
  } else if (Object.keys(p).length) {
    row.raw = p
  }
  row.initialDelay = p.initialDelaySeconds ?? 0
  row.period = p.periodSeconds ?? 10
  row.timeout = p.timeoutSeconds ?? 1
  row.success = p.successThreshold ?? 1
  row.failure = p.failureThreshold ?? 3
  return row
}
// 暂存 payload 的探针（type/path/scheme/port/command + 秒数阈值）→ 表单行
function probeFromPayload(pp: any) {
  const row = newProbeRow()
  if (!pp || typeof pp !== 'object') return row
  row.type = pp.type || 'none'
  row.path = pp.path || '/'
  row.port = pp.port ?? ''
  row.scheme = pp.scheme === 'HTTPS' ? 'HTTPS' : 'HTTP'
  row.command = Array.isArray(pp.command) ? pp.command.map((s: string) => /\s/.test(s) ? `"${s}"` : s).join(' ') : ''
  row.initialDelay = pp.initialDelaySeconds ?? 0
  row.period = pp.periodSeconds ?? 10
  row.timeout = pp.timeoutSeconds ?? 1
  row.success = pp.successThreshold ?? 1
  row.failure = pp.failureThreshold ?? 3
  return row
}
// 表单行 → 暂存/保存 payload 的探针：null = 删除该探针；undefined = 原样保留（罕见类型 raw）
function probeToPayload(p: any, kind?: string): any {
  if (p?.raw) return undefined
  if (!p || p.type === 'none') return null
  const o: any = {
    type: p.type,
    initialDelaySeconds: Number(p.initialDelay) || 0,
    periodSeconds: Number(p.period) || 10,
    timeoutSeconds: Number(p.timeout) || 1,
    failureThreshold: Number(p.failure) || 3,
  }
  // 就绪探针允许 successThreshold > 1（连续 N 次成功才恢复流量）；
  // 存活/启动探针 K8s 恒为 1，只在非 1 时发送（防御异常值，后端钳制回 1）
  if (kind === 'readiness' || (Number(p.success) || 1) > 1) o.successThreshold = Number(p.success) || 1
  if (p.type === 'http') {
    o.path = String(p.path || '/')
    o.port = p.port
    o.scheme = p.scheme === 'HTTPS' ? 'HTTPS' : 'HTTP'
  } else if (p.type === 'tcp') {
    o.port = p.port
  } else {
    o.command = shSplit(p.command || '').filter(Boolean)
  }
  return o
}

function buildForm(obj: any) {
  // 标签 / 注解 → 行编辑（对象键序在服务端已排序，稳定展示）
  metaForm.labels = Object.entries(obj.metadata?.labels || {}).map(([key, value]) => ({ key, value: String(value) }))
  metaForm.annotations = Object.entries(obj.metadata?.annotations || {}).map(([key, value]) => ({ key, value: String(value) }))
  if (cur.resource === 'services') {
    svcForm.ports = (obj.spec?.ports || []).map((p: any) => ({
      name: p.name || '', port: p.port ?? null, targetPort: p.targetPort ?? '', protocol: p.protocol || 'TCP', nodePort: p.nodePort ?? null,
    }))
    svcForm.selector = Object.entries(obj.spec?.selector || {}).map(([k, v]) => ({ key: k, value: String(v) }))
    return
  }
  if (cur.resource === 'ingresses') {
    // spec.rules 展开为行：同 rule 的各 path 一行；端口可能是 number 或名称
    const rows: any[] = []
    for (const rule of obj.spec?.rules || []) {
      for (const p of rule.http?.paths || []) {
        rows.push({
          host: rule.host || '',
          path: p.path || '/',
          pathType: p.pathType || 'Prefix',
          svc: p.backend?.service?.name || p.backend?.resource?.name || '',
          port: p.backend?.service?.port?.number ?? p.backend?.service?.port?.name ?? '',
        })
      }
    }
    ingForm.rows = rows
    return
  }
  if (cur.resource === 'configmaps' || cur.resource === 'secrets') {
    // data 为顶层字段；Secret 的值是 base64，解码展示、保存重编码（往返无损）；
    // 解码失败 = 二进制内容 → 只读原样保留；stringData 同键覆盖（apiserver 语义：stringData 合并进 data）
    const isSecret = cur.resource === 'secrets'
    const rows: Array<{ key: string; value: string; binary?: boolean }> = []
    const put = (k: string, v: any, enc: boolean) => {
      let value = String(v ?? '')
      let binary = false
      if (enc) {
        try { value = decodeURIComponent(escape(atob(value))) } catch { binary = true }
      }
      const hit = rows.find((r) => r.key === k)
      if (hit) { hit.value = value; hit.binary = binary }
      else rows.push({ key: k, value, binary })
    }
    for (const [k, v] of Object.entries(obj.data || {})) put(k, v, isSecret)
    if (isSecret) for (const [k, v] of Object.entries(obj.stringData || {})) put(k, v, false)
    dataForm.rows = rows
    return
  }
  if (cur.resource === 'persistentvolumeclaims') {
    pvcCapForm.cur = obj.spec?.resources?.requests?.storage || ''
    pvcCapForm.size = pvcCapForm.cur
    pvcCapForm.storageClass = obj.spec?.storageClassName || ''
    pvcCapForm.accessMode = (obj.spec?.accessModes || []).join(' / ')
    return
  }
  if (cur.resource === 'nodes') {
    // 节点污点读回（spec.taints 全量覆盖编辑；系统污点行同样展示，保存时前后端都会拦）
    taintForm.rows = (obj.spec?.taints || []).map((t: any) => ({ key: t.key || '', value: t.value || '', effect: t.effect || 'NoSchedule' }))
    return
  }
  const ps = cur.resource === 'pods' ? obj.spec
    : cur.resource === 'cronjobs' ? obj.spec?.jobTemplate?.spec?.template?.spec
    : obj.spec?.template?.spec
  // 存储挂载：解析 pod 级卷（可建模的进表单，其余保留），init 容器引用的卷名集合用于保存时保底
  volState.raw = (ps?.volumes || []) as any[]
  volState.modeled = {}
  for (const v of volState.raw) {
    if (v?.persistentVolumeClaim?.claimName) volState.modeled[v.name] = { type: 'pvc', refName: v.persistentVolumeClaim.claimName }
    else if (v?.configMap?.name) volState.modeled[v.name] = { type: 'configmap', refName: v.configMap.name }
    else if (v?.secret?.secretName) volState.modeled[v.name] = { type: 'secret', refName: v.secret.secretName }
    else if (v?.emptyDir) volState.modeled[v.name] = { type: 'emptydir', refName: '' }
  }
  volState.initNames = new Set(((ps?.initContainers || []) as any[]).flatMap((c: any) => c.volumeMounts || []).map((m: any) => m.name))
  formModel.value = (ps?.containers || []).map((c: any) => ({
    name: c.name || '',
    image: c.image || '',
    ...cmdFormVals(c.command, c.args),
    ports: (c.ports || []).map((p: any) => ({ name: p.name || '', containerPort: p.containerPort ?? null, hostPort: p.hostPort ?? null, protocol: p.protocol || 'TCP' })),
    resources: {
      limits: { cpu: qtyObj(c.resources?.limits?.cpu, 'cpu'), memory: qtyObj(c.resources?.limits?.memory, 'mem') },
      requests: { cpu: qtyObj(c.resources?.requests?.cpu, 'cpu'), memory: qtyObj(c.resources?.requests?.memory, 'mem') },
      extended: extendedRows(c.resources?.limits),
    },
    env: (c.env || []).map(envToRow),
    probes: {
      liveness: probeFromK8s(c.livenessProbe),
      readiness: probeFromK8s(c.readinessProbe),
      startup: probeFromK8s(c.startupProbe),
    },
    mounts: (c.volumeMounts || []).filter((m: any) => volState.modeled[m.name]).map((m: any) => ({
      type: volState.modeled[m.name].type,
      refName: volState.modeled[m.name].refName,
      mountPath: m.mountPath || '',
      subPath: m.subPath || '',
      readOnly: !!m.readOnly,
      volName: m.name, // 沿用原卷名；改引用对象 = 更新卷定义而非换卷
    })),
    keptMounts: (c.volumeMounts || []).filter((m: any) => !volState.modeled[m.name]), // 不支持类型原样保留
  }))
  // 调度策略读回：Pod 级 affinity → UI 行模型；含建模外结构时置警示（保存会被后端拒绝，提示走 YAML）
  const sched = schedFromK8s(ps)
  schedForm.value = sched.model
  schedUnmodeledFlag.value = sched.unmodeled
}

async function refreshDetail() {
  const row = detailTarget.value
  if (!row) return
  const res = await getResource(cluster.value, {
    group: cur.group, version: cur.version, resource: cur.resource,
    namespace: row.metadata.namespace || '', name: row.metadata.name,
  })
  detailObj.value = res.data
  detailYaml.value = yaml.dump(res.data, { noRefs: true, lineWidth: -1 })
  buildForm(res.data)
  if (cur.resource === 'ingresses') {
    // 路由编辑的后端服务下拉：当前命名空间的 Service 名（与白屏创建同源）
    try {
      const r = await listResources(cluster.value, { group: '', version: 'v1', resource: 'services', namespace: row.metadata.namespace || '', _silent: 1 })
      svcNameOptions.value = (r.data.items || []).map((i: any) => i.metadata.name)
    } catch { svcNameOptions.value = [] }
  }
  if (['deployments', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs'].includes(cur.resource) || cur.resource === 'nodes') {
    // 调度候选（工作负载的调度策略 + 节点的污点编辑行下拉）：权限=工作负载 update+scheduling 或 nodes 相关，静默失败
    loadNodeOptions()
  }
}

async function loadNodeOptions() {
  if (!cluster.value) return
  try {
    // 专用调度候选端点：权限=工作负载的 update+scheduling 动作（一般用户无需 nodes 权限）；
    // 无权限返回空集——下拉无选项仍可手输
    const r = await schedOptions(cluster.value)
    nodeOptions.value = r.data.nodes || []
    nodeLabelKeys.value = r.data.label_keys || []
    nodeLabelValues.value = r.data.label_values || {}
    taintKeys.value = r.data.taint_keys || []
    taintValues.value = r.data.taint_values || {}
  } catch {
    nodeOptions.value = []
    nodeLabelKeys.value = []
    nodeLabelValues.value = {}
    taintKeys.value = []
    taintValues.value = {}
  }
}

async function openDetail(row: any) {
  detailTarget.value = row
  // nodes 主显示名同列表（IP），原主机名括注保留可对照
  if (cur.resource === 'nodes' && nodeIp(row) && nodeIp(row) !== row.metadata.name) {
    detailTitle.value = `${row.kind} / ${nodeIp(row)}（${row.metadata.name}）`
  } else {
    detailTitle.value = `${row.kind} / ${row.metadata.namespace ? row.metadata.namespace + '/' : ''}${row.metadata.name}`
  }
  await refreshDetail()
  loadDrafts() // 暂存草稿（异步，不阻塞打开）
  // 默认落在第一个「有编辑权限」的模块（无权限模块为只读展示）
  formTab.value = formSections.value.find((s) => sectionEditable(s.key))?.key || formSections.value[0]?.key || 'image'
  // tab 顺序：运行实例 → 应用编辑 → YAML；默认落在运行实例（无该 tab 时回退应用编辑/YAML）
  // nodes：概要 → YAML（概要含角色/资源/分配明细，打开即加载）
  detailTab.value = hasPodsView.value ? 'pods' : (cur.resource === 'nodes' ? 'overview' : (formSections.value.length ? 'form' : 'yaml'))
  if (hasPodsView.value) loadAppPods() // 默认 tab 即运行实例，tab-change 不会触发，主动加载
  if (cur.resource === 'nodes') loadOverview() // 同上：默认 tab 即概要
  detailDlg.value = true
}

// 数字/字符串端口归一：纯数字转 number，名称（如 http）保持字符串
function portVal(v: any) {
  if (v === null || v === undefined || v === '') return undefined
  if (typeof v === 'number') return v
  return /^\d+$/.test(String(v)) ? Number(v) : String(v)
}

function cleanRes(r: any) {
  const out: Record<string, string> = {}
  const cpu = qtyStr(r.cpu)
  const mem = qtyStr(r.memory)
  if (cpu) out.cpu = cpu
  if (mem) out.memory = mem
  return out
}

function extendedRows(resources: any) {
  return Object.entries(resources || {})
    .filter(([key]) => key !== 'cpu' && key !== 'memory')
    .map(([key, value]) => ({ key, value: value == null ? '' : String(value) }))
}

// 表单编辑防呆：按当前模块校验，返回第一条错误文案（null=通过）。
// 拦在保存前：非法表单打到 apiserver 才报错时是一长串英文，且部分问题（选择器重复键）会被静默吞掉。
function validateFormSection(section: string): string | null {
  if (section === 'labels' || section === 'annotations') {
    return validateMetaForm(section)
  }
  if (section === 'taints') {
    // 与后端 validateTaint 同规则的前置校验；系统污点行不做拦截（必须原样提交才能保留，删改由后端对照现有值拒绝）
    const keyRe = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
    const valRe = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
    const segRe = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/
    const seen = new Set<string>()
    for (let i = 0; i < taintForm.rows.length; i++) {
      const t = taintForm.rows[i]
      const key = t.key.trim()
      if (!key) continue
      let name = key
      if (key.includes('/')) {
        const parts = key.split('/')
        if (parts.length !== 2 || !parts[0] || !parts[1]) return `第 ${i + 1} 行污点键「${key}」不合法（前缀只能有一段 /）`
        if (parts[0].length > 253 || parts[0].split('.').some((s) => !segRe.test(s))) {
          return `第 ${i + 1} 行污点键「${key}」的前缀需为 DNS 子域（小写字母/数字/中划线/点）`
        }
        name = parts[1]
      }
      if (!name || name.length > 63 || !keyRe.test(name)) {
        return `第 ${i + 1} 行污点键「${key}」不合法（名称 ≤63 字符，字母/数字开头结尾）`
      }
      if (t.value.trim() && (t.value.trim().length > 63 || !valRe.test(t.value.trim()))) {
        return `第 ${i + 1} 行污点值不合法（≤63 字符，字母或数字开头结尾，可含 - _ .）`
      }
      if (!['NoSchedule', 'PreferNoSchedule', 'NoExecute'].includes(t.effect)) {
        return `第 ${i + 1} 行 effect 不合法`
      }
      const k2 = `${key}/${t.effect}`
      if (seen.has(k2)) return `第 ${i + 1} 行污点 ${key}（${t.effect}）重复`
      seen.add(k2)
    }
    return null
  }
  if (section === 'scheduling') {
    if (schedUnmodeledFlag.value) {
      return '存在表单不支持的高级亲和配置（多 term / matchFields / Pod 亲和等），保存将被拒绝，请使用 YAML 编辑'
    }
    return validateSched(schedForm.value) || null
  }
  if (cur.resource === 'services') {
    if (section === 'ports') {
      const names = new Set<string>()
      for (const p of svcForm.ports) {
        if (p.port === null || p.port === undefined || p.port === '') continue
        if (!p.name) continue
        if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(p.name) || p.name.length > 15) {
          return `端口名「${p.name}」需为小写字母/数字/中划线且不超过 15 字符`
        }
        if (names.has(p.name)) return `端口名「${p.name}」重复`
        names.add(p.name)
      }
      return null
    }
    const keys = new Set<string>()
    for (const s of svcForm.selector) {
      if (!s.key.trim()) continue
      if (!/^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?(\/[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$/.test(s.key.trim())) {
        return `选择器键「${s.key}」不是合法的标签键`
      }
      if (keys.has(s.key.trim())) return `选择器键「${s.key}」重复，重复行会被静默丢弃`
      keys.add(s.key.trim())
    }
    return null
  }
  if (cur.resource === 'ingresses') {
    const seen = new Set<string>()
    for (let i = 0; i < ingForm.rows.length; i++) {
      const r = ingForm.rows[i]
      const host = (r.host || '').trim(), path = (r.path || '').trim(), svc = (r.svc || '').trim(), port = String(r.port ?? '').trim()
      if (!path) return `第 ${i + 1} 行的路径不能为空`
      if (!path.startsWith('/')) return `第 ${i + 1} 行的路径 ${path} 需以 / 开头`
      if (host && !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(host)) return `第 ${i + 1} 行的域名 ${host} 不合法`
      if (!svc) return `第 ${i + 1} 行的后端服务不能为空`
      if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(svc) || svc.length > 63) return `第 ${i + 1} 行的后端服务名 ${svc} 不合法`
      if (!port) return `第 ${i + 1} 行的端口不能为空`
      if (/^\d+$/.test(port)) {
        const n = Number(port)
        if (n < 1 || n > 65535) return `第 ${i + 1} 行的端口需为 1-65535`
      } else if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(port)) {
        return `第 ${i + 1} 行的端口 ${port} 需为 1-65535 数字或端口名`
      }
      const key = `${host}|${path}`
      if (seen.has(key)) return `路由 ${host}${path} 重复`
      seen.add(key)
    }
    return null
  }
  if (cur.resource === 'configmaps' || cur.resource === 'secrets') {
    const keys = new Set<string>()
    for (const r of dataForm.rows) {
      if (!r.key.trim()) continue
      if (!/^[-._a-zA-Z0-9]+$/.test(r.key.trim())) return `数据键「${r.key}」不合法（字母/数字/中划线/点/下划线）`
      if (keys.has(r.key.trim())) return `数据键「${r.key}」重复`
      keys.add(r.key.trim())
    }
    return null
  }
  if (cur.resource === 'persistentvolumeclaims') {
    const size = pvcCapForm.size.trim()
    if (!/^\d+(\.\d+)?(Ki|Mi|Gi|Ti)?$/.test(size)) return '容量格式如 20Gi / 500Mi'
    if (pvcCapForm.cur) {
      const nv = qtyNum(size, 'mem'), cv = qtyNum(pvcCapForm.cur, 'mem')
      if (nv != null && cv != null && nv < cv) return `新容量（${size}）不能小于当前容量（${pvcCapForm.cur}）`
    }
    return null
  }
  for (let i = 0; i < formModel.value.length; i++) {
    const ct = formModel.value[i]
    const tag = formModel.value.length > 1 ? `容器 ${i + 1}（${ct.name}）：` : ''
    if (section === 'image') {
      const img = String(ct.image || '').trim()
      if (!img) return `${tag}镜像不能为空`
      if (/\s/.test(img) || !/^[\x21-\x7e]+$/.test(img)) return `${tag}镜像地址含空格或非 ASCII 字符`
      if (!/^[a-z0-9._\-/:@]+$/.test(img)) return `${tag}镜像地址格式不对（应为 registry/repo:tag 形式）`
    }
    if (section === 'command') {
      // 入口按空格分词，引号不闭合会被静默吞掉，提前拦下；命令无需引号（脚本原样 / 每行一个参数）
      const entry = String(ct.command || '').trim()
      if ((entry.match(/"/g)?.length || 0) % 2 || (entry.match(/'/g)?.length || 0) % 2) {
        return `${tag}入口的引号未闭合`
      }
    }
    if (section === 'ports') {
      const names = new Set<string>()
      const seen = new Set<string>()
      const hostPorts = new Set<number>()
      for (const p of ct.ports) {
        if (p.containerPort === null || p.containerPort === undefined) continue
        if (p.name) {
          if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(p.name) || p.name.length > 15) {
            return `${tag}端口名「${p.name}」需为小写字母/数字/中划线且不超过 15 字符`
          }
          if (names.has(p.name)) return `${tag}端口名「${p.name}」重复`
          names.add(p.name)
        }
        const key = `${p.containerPort}/${p.protocol || 'TCP'}`
        if (seen.has(key)) return `${tag}容器端口 ${p.containerPort}（${p.protocol || 'TCP'}）重复`
        seen.add(key)
        if (p.hostPort) {
          if (p.hostPort < 1 || p.hostPort > 65535) return `${tag}主机端口 ${p.hostPort} 需为 1-65535`
          if (hostPorts.has(p.hostPort)) return `${tag}主机端口 ${p.hostPort} 重复（同一 Pod 内不可重复占用宿主机端口）`
          hostPorts.add(p.hostPort)
        }
      }
    }
    if (section === 'resources') {
      const boundsError = formResourceBoundsError(ct)
      if (boundsError) return `${tag}${boundsError}`
      const seen = new Set<string>()
      for (const e of ct.resources.extended || []) {
        const key = e.key?.trim() || ''
        if (!key) {
          if (e.value !== '' && e.value != null) return `${tag}异构资源名不能为空`
          continue
        }
        if (key === 'cpu' || key === 'memory') return `${tag}异构资源名不能使用 CPU / 内存，请使用上方对应输入框`
        if (!/^[A-Za-z0-9][A-Za-z0-9.-]*\/[A-Za-z0-9][A-Za-z0-9._-]*$/.test(key)) {
          return `${tag}异构资源名「${key}」需形如 huawei.com/Ascend910`
        }
        if (!/^\d+$/.test(String(e.value ?? '').trim())) return `${tag}异构资源「${key}」的数量需为整数`
        if (seen.has(key)) return `${tag}异构资源「${key}」重复`
        seen.add(key)
      }
    }
    if (section === 'env') {
      const names = new Set<string>()
      for (const e of ct.env) {
        if (!e.name.trim()) continue
        if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(e.name.trim())) {
          return `${tag}环境变量名「${e.name}」不合法（需字母开头，仅字母/数字/下划线）`
        }
        if (names.has(e.name.trim())) return `${tag}环境变量「${e.name}」重复`
        names.add(e.name.trim())
        if ((e.type === 'secret' || e.type === 'configmap') && (!e.refName.trim() || !e.refKey.trim())) {
          return `${tag}环境变量「${e.name}」的引用名 / 键未填完整（类型为 ${e.type === 'secret' ? 'Secret' : 'ConfigMap'}）`
        }
        if (e.type === 'fieldref' && !e.refKey.trim()) {
          return `${tag}环境变量「${e.name}」的字段引用路径未填写（如 metadata.name）`
        }
      }
    }
    if (section === 'probes') {
      for (const pk of probeKeys) {
        const p = ct.probes?.[pk]
        if (!p || p.type === 'none' || p.raw) continue
        const label = probeMeta[pk].label
        const portStr = String(p.port ?? '').trim()
        if (p.type === 'http' || p.type === 'tcp') {
          if (!portStr) return `${tag}${label}端口未填写`
          if (/^\d+$/.test(portStr) ? (+portStr < 1 || +portStr > 65535)
            : (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(portStr) || portStr.length > 15)) {
            return `${tag}${label}端口需为 1-65535 数字或端口名（小写字母/数字/中划线，≤15 字符）`
          }
        }
        if (p.type === 'http' && p.path && !String(p.path).trim().startsWith('/')) {
          return `${tag}${label}HTTP 路径需以 / 开头（如 /healthz）`
        }
        if (p.type === 'exec' && !String(p.command || '').trim()) {
          return `${tag}${label}命令未填写`
        }
      }
    }
    if (section === 'storage') {
      const paths = new Set<string>()
      for (const m of ct.mounts || []) {
        if (!m.mountPath || !m.mountPath.startsWith('/')) return `${tag}挂载路径需以 / 开头（如 /data）`
        if (paths.has(m.mountPath)) return `${tag}挂载路径 ${m.mountPath} 重复`
        paths.add(m.mountPath)
        if (m.type !== 'emptydir' && !String(m.refName || '').trim()) {
          return `${tag}类型为 ${m.type} 的挂载需填写引用对象名`
        }
        if (m.subPath && (m.subPath.startsWith('/') || m.subPath.includes('..'))) {
          return `${tag}子路径不能以 / 开头或包含 ..`
        }
      }
    }
  }
  return null
}

async function saveFormSection() {
  const row = detailTarget.value
  if (!row) return
  const section = formTab.value
  const verr = validateFormSection(section)
  if (verr) { ElMessage.warning(verr); return }
  // 软提示不拦截：镜像未固定 tag 会拉 latest
  if (section === 'image' && cur.resource !== 'services') {
    const noTag = formModel.value
      .filter((c: any) => c.image.trim() && !c.image.trim().split('/').pop()!.includes(':'))
      .map((c: any) => c.image.trim())
    if (noTag.length) ElMessage.warning(`镜像 ${noTag.join('、')} 未指定 tag，将拉取 latest，建议固定版本`)
  }
  const target = {
    group: cur.group, version: cur.version, resource: cur.resource,
    namespace: row.metadata.namespace || '', name: row.metadata.name,
  }
  const payload = buildSectionPayload(section)
  if (payload === null) return
  formSaving.value = true
  try {
    await resourceFormEdit(cluster.value, target, section, payload)
    ElMessage.success(`已保存${currentSectionLabel.value}`)
    await refreshDetail()
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { formSaving.value = false }
}

async function saveDetail() {
  const row = detailTarget.value
  if (!row) return
  let obj: any
  try {
    obj = yaml.load(detailYaml.value)
  } catch (e: any) {
    ElMessage.error('YAML 语法错误: ' + e.message)
    return
  }
  saving.value = true
  try {
    await updateResource(cluster.value, {
      group: cur.group, version: cur.version, resource: cur.resource,
      namespace: row.metadata.namespace || '', name: row.metadata.name,
    }, obj)
    ElMessage.success('已保存')
    detailDlg.value = false
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { saving.value = false }
}

async function doDelete(row: any) {
  const name = row.metadata.name
  try {
    // 防误触：需输入名称确认
    await ElMessageBox.prompt(
      `该操作不可恢复。请输入名称 "${name}" 以确认删除：`,
      '删除确认', {
        type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消',
        inputValidator: (v: string) => v.trim() === name || '输入的名称不匹配',
      },
    )
  } catch { return }
  try {
    await deleteResource(cluster.value, {
      group: cur.group, version: cur.version, resource: cur.resource,
      namespace: row.metadata.namespace || '', name: row.metadata.name,
    })
    ElMessage.success('已删除')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

// 删除单个运行实例（Pod）；应用未停止时 K8s 会自动重建
async function doDeletePod(row: any) {
  try {
    await ElMessageBox.confirm(
      `确定删除运行实例 "${row.metadata.name}" 吗？若所属应用仍在运行，K8s 会自动重建一个新实例。`,
      '删除确认', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch { return }
  try {
    await deleteResource(cluster.value, {
      group: '', version: 'v1', resource: 'pods',
      namespace: row.metadata.namespace || '', name: row.metadata.name,
    })
    ElMessage.success('已删除，实例列表稍后刷新')
    setTimeout(loadAppPods, 800)
  } catch (e) { ElMessage.error(errMsg(e)) }
}

async function doRestart(row: any) {
  try {
    await ElMessageBox.confirm(`确定滚动重启 "${row.metadata.name}" 吗？`, '重启确认', { type: 'warning' })
  } catch { return }
  try {
    await restartResource(cluster.value, {
      group: cur.group, version: cur.version, resource: cur.resource,
      namespace: row.metadata.namespace, name: row.metadata.name,
    })
    ElMessage.success('重启指令已下发')
  } catch (e) { ElMessage.error(errMsg(e)) }
}

async function doScale(row: any) {
  try {
    const { value } = await ElMessageBox.prompt('目标副本数', '伸缩', {
      inputValue: String(row.spec?.replicas ?? 0),
      inputPattern: /^\d+$/, inputErrorMessage: '请输入非负整数',
    })
    await scaleResource(cluster.value, {
      group: cur.group, version: cur.version, resource: cur.resource,
      namespace: row.metadata.namespace, name: row.metadata.name, replicas: Number(value),
    })
    ElMessage.success('已调整')
    load()
  } catch (e) {
    if (e !== 'cancel' && (e as any)?.message !== undefined) ElMessage.error(errMsg(e))
  }
}

// 定时任务创建入口：重启 / 扩缩容有对应动作权限即可建（后端同口径复核）；
// 打开期间置 scheduleDlgOpen，暂停表格自动刷新（避免输入时光标被重置）
const scheduleDlg = ref()
function openSchedule(row: any) {
  scheduleDlgOpen.value = true
  scheduleDlg.value.open(cluster.value, row.metadata.namespace, cur.resource, row.metadata.name)
}

// 多容器 Pod 的默认容器：优先 kubectl.kubernetes.io/default-container 注解（kubectl 同款语义），
// 无注解或注解失效时取第一个；与文件浏览器（openFileBrowser）行为一致
function defaultContainer(row: any): string {
  const names = (row.spec?.containers || []).map((c: any) => c.name)
  const ann = row.metadata?.annotations?.['kubectl.kubernetes.io/default-container']
  if (ann && names.includes(ann)) return ann
  return names[0] || ''
}

function openLogs(row: any) {
  logsTarget.value = {
    cluster: cluster.value,
    namespace: row.metadata.namespace,
    pod: row.metadata.name,
    containers: (row.spec?.containers || []).map((c: any) => c.name),
  }
  logContainer.value = defaultContainer(row)
  logsDlg.value = true
}

// ---------- 标签/注解校验（K8s 元数据格式，前后端同款规则）----------
const LABEL_PREFIX_SEG_RE = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/
const LABEL_NAME_RE = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
const LABEL_VALUE_RE = /^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$/

// 校验标签/注解表单行：空行忽略、键格式、值格式、重复键；返回错误文案（null=通过）
function validateMetaForm(section: 'labels' | 'annotations'): string | null {
  const rows = metaForm[section]
  const seen = new Set<string>()
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i]
    const key = String(r.key || '').trim()
    const value = String(r.value ?? '')
    if (!key && !value) continue // 整行没填的忽略
    const tag = `第 ${i + 1} 行：`
    if (!key) return `${tag}键不能为空`
    // 键 = 可选前缀/名称
    let name = key
    const slash = key.lastIndexOf('/')
    if (slash >= 0) {
      const prefix = key.slice(0, slash)
      name = key.slice(slash + 1)
      if (!prefix || !name) return `${tag}键「${key}」的前缀或名称为空`
      if (prefix.length > 253) return `${tag}键「${key}」的前缀超过 253 字符`
      for (const seg of prefix.split('.')) {
        if (!LABEL_PREFIX_SEG_RE.test(seg)) {
          return `${tag}键「${key}」的前缀需为 DNS 子域（小写字母/数字/中划线/点）`
        }
      }
    }
    if (!name || name.length > 63 || !LABEL_NAME_RE.test(name)) {
      return `${tag}键「${key}」不合法（≤63 字符，字母或数字开头结尾，可含 - _ . 与可选域名前缀）`
    }
    if (seen.has(key)) return `${tag}键「${key}」与其他行重复`
    seen.add(key)
    if (section === 'labels') {
      if (value.length > 63 || !LABEL_VALUE_RE.test(value)) {
        return `${tag}标签「${key}」的值不合法（≤63 字符，字母或数字开头结尾，可含 - _ .）`
      }
    } else if (/[\x00-\x1f]/.test(value.replace(/[\t\n\r]/g, ''))) {
      return `${tag}注解「${key}」的值含控制字符，无法保存`
    }
  }
  return null
}

// 标签/注解行 → 对象（过滤空行；返回 null 表示全空 = 删除整个子表）
function metaRowsToObj(section: 'labels' | 'annotations'): Record<string, string> | null {
  const out: Record<string, string> = {}
  for (const r of metaForm[section]) {
    const key = String(r.key || '').trim()
    if (!key && !String(r.value ?? '')) continue
    out[key] = String(r.value ?? '')
  }
  return Object.keys(out).length ? out : null
}

// 下载容器内文件：文件浏览器勾选下载（浏览 ls -1pa，单文件 cat、多文件 tar 打包）
const fileDlg = ref(false)
const fileRow = ref<any>(null)
const fileContainer = ref('')
const filePath = ref('/') // 当前目录
const fileEntries = ref<any[]>([])
const fileSel = ref<string[]>([]) // 勾选的文件名（同目录）
const fileLoading = ref(false)
const downloading = ref(false)
const fileUploadInput = ref<HTMLInputElement>() // 隐藏的原生文件选择框（el-upload 对简单场景过重）
const uploading = ref(false)

function fileContainers(): string[] {
  return (fileRow.value?.spec?.containers || []).map((c: any) => c.name)
}

async function loadDir() {
  fileLoading.value = true
  fileSel.value = []
  try {
    const row = fileRow.value
    const res = await listPodFiles(cluster.value, row.metadata.namespace, row.metadata.name, filePath.value, fileContainer.value)
    fileEntries.value = res.data.entries || []
  } catch (e) {
    fileEntries.value = []
    ElMessage.error(errMsg(e))
  } finally { fileLoading.value = false }
}

function openFileBrowser(row: any) {
  fileRow.value = row
  fileContainer.value = defaultContainer(row)
  filePath.value = '/'
  fileDlg.value = true
  loadDir()
}

function onFileContainerChange() { filePath.value = '/'; loadDir() }

function joinDir(name: string): string {
  const cur = filePath.value.replace(/\/+$/, '')
  return (cur || '') + '/' + name
}

function enterDir(name: string) {
  filePath.value = joinDir(name)
  loadDir()
}

function parentDir() {
  const cur = filePath.value.replace(/\/+$/, '')
  const i = cur.lastIndexOf('/')
  filePath.value = i <= 0 ? '/' : cur.slice(0, i)
  loadDir()
}

// ---------- 容器内文件上传（与下载分开授权，目标为当前目录）----------
function pickUpload() {
  fileUploadInput.value?.click()
}

async function onUploadPick(ev: Event) {
  const input = ev.target as HTMLInputElement
  const files = Array.from(input.files || [])
  input.value = '' // 清空以允许连续选择同一文件
  if (!files.length) return
  const row = fileRow.value
  uploading.value = true
  try {
    for (const f of files) { // 逐个提交（后端单文件接口），全部串行避免撑爆 exec 并发
      await uploadPodFile(cluster.value, row.metadata.namespace, row.metadata.name, filePath.value, f, fileContainer.value)
      ElMessage.success(`已上传 ${f.name}`)
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    uploading.value = false
    loadDir() // 无论成败都刷新（可能部分成功）
  }
}

async function downloadSelected() {
  const names = fileSel.value.filter((n) => !fileEntries.value.some((x) => x.name === n && x.dir))
  if (!names.length) { ElMessage.warning('请先勾选要下载的文件'); return }
  const row = fileRow.value
  const paths = names.map((n) => joinDir(n))
  downloading.value = true
  try {
    const res = await downloadPodFile(cluster.value, row.metadata.namespace, row.metadata.name, paths, fileContainer.value)
    const name = paths.length === 1
      ? (paths[0].split('/').filter(Boolean).pop() || 'download')
      : `${row.metadata.name}-files.tar`
    const url = URL.createObjectURL(res.data)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.click()
    URL.revokeObjectURL(url)
    ElMessage.success(`已下载 ${name}`)
  } catch (e: any) {
    // blob 错误响应需解析为文本提示
    if (e?.response?.data instanceof Blob) {
      e.response.data.text().then((t: string) => {
        try { ElMessage.error(JSON.parse(t).message || '下载失败') } catch { ElMessage.error('下载失败') }
      })
    } else {
      ElMessage.error(errMsg(e))
    }
  } finally { downloading.value = false }
}

function openTerminal(row: any) {
  termTarget.value = {
    cluster: cluster.value,
    namespace: row.metadata.namespace,
    pod: row.metadata.name,
    containers: (row.spec?.containers || []).map((c: any) => c.name),
  }
  termContainer.value = defaultContainer(row)
  termDlg.value = true
}

async function doApply(dryRun: boolean) {
  if (!deployYaml.value.trim()) {
    ElMessage.warning('请输入 YAML')
    return
  }
  deploying.value = true
  deployResults.value = []
  try {
    const res = await applyYAML(cluster.value, {
      yaml: deployYaml.value, dry_run: dryRun, namespace: deployNS.value,
    })
    deployResults.value = res.data.results || []
    toastResults(deployResults.value)
    if (!dryRun && res.data.ok) load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { deploying.value = false }
}

// 部署结果逐条以屏幕顶部居中 toast 展示（列表里不一定看得到底部的结果区）
function toastResults(results: any[]) {
  for (const r of results) {
    const msg = `${r.kind}/${r.name}（${r.namespace}）：${r.message}`
    if (r.ok) {
      ElMessage.success({ message: msg, duration: 4000 })
    } else {
      ElMessage.error({ message: msg, duration: 8000, showClose: true })
    }
  }
}

onMounted(async () => {
  try {
    // me/grants 现含已过期条目（供「我的权限」展示），导航/按钮过滤须剔除已失效授权
    myPerms.value = ((await myGrants()).data || []).filter(
      (r: any) => !r.expires_at || new Date(r.expires_at).getTime() > Date.now())
  } catch { myPerms.value = [] }
  await loadClusters()
  ensureSelection()
  applyRouteQuery() // 深链定位命名空间/资源（在导航与命名空间就绪后）
  saveResourceSelection(true)
  // myPerms 加载完成后再加载 CR（按授权过滤）
  if (cluster.value) await loadCRs(cluster.value)
  if (cluster.value) load()
})
</script>

<style scoped>
.res-page { display: flex; gap: 12px; height: calc(100vh - 88px); }
.nav-card { width: 200px; flex-shrink: 0; overflow: auto; }
.nav-menu { border-right: none; }
/* 分组标题：与菜单项视觉区分 */
.nav-menu :deep(.el-sub-menu__title) {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-regular);
  background: var(--el-fill-color-extra-light);
}
.nav-menu :deep(.el-menu-item) { font-size: 13px; min-width: 0; }
.res-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; flex-wrap: wrap; }
.current-res { color: #606266; font-size: 13px; }
.res-count { color: #909399; font-size: 12px; white-space: nowrap; cursor: default; }
.dim { color: #909399; font-size: 12px; margin-left: 6px; }
.mono { font-family: ui-monospace, Consolas, monospace; }
/* Describe 弹窗：等宽纯文本，独立滚动 */
.describe-box { min-height: 200px; max-height: calc(100vh - 220px); overflow: auto; }
.describe-box pre { margin: 0; white-space: pre-wrap; word-break: break-all; font-size: 12.5px; line-height: 1.65; color: #303133; }
.drawer-body { display: flex; flex-direction: column; height: 100%; }
.drawer-actions { display: flex; justify-content: flex-end; gap: 8px; padding-top: 10px; }

/* 暂存 banner：文案与操作按钮同行 */
.draft-banner { display: flex; align-items: center; justify-content: space-between; gap: 12px; width: 100%; flex-wrap: wrap; }
.draft-banner .draft-actions { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.drawer-body :deep(.editor) { flex: 1; min-height: 0; }
.detail-tabs { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.detail-tabs :deep(.el-tabs__content) { flex: 1; min-height: 0; overflow: auto; }
.detail-tabs :deep(.el-tab-pane) { height: 100%; }
/* 详情抽屉页签指示条：弹性滑行（贝塞尔过冲曲线，切换时有胶水吸附感，设计稿 #4） */
.detail-tabs :deep(.el-tabs__active-bar) { transition: transform .35s cubic-bezier(.34, 1.56, .64, 1), width .35s cubic-bezier(.34, 1.56, .64, 1); }
.form-tabs :deep(.el-tabs__content) { overflow: visible; }
/* 应用编辑子项放大 */
.form-edit :deep(.el-table) { font-size: 14px; }
.form-edit :deep(.el-table th.el-table__cell) { font-size: 13px; }
.form-edit :deep(.el-tabs__item) { font-size: 14px; height: 44px; line-height: 44px; }
.ct-block { margin-bottom: 18px; padding: 16px; border: 1px solid #ebeef5; border-radius: 6px; }
/* 健康检查（探针）模块：四段卡片——标题行 + 探测方式行 + 阈值网格 + 说明行 */
.probe-card { border: 1px solid var(--el-border-color-lighter); border-radius: 6px; padding: 14px 16px; margin-bottom: 12px; }
.probe-head { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
.probe-title { font-weight: 600; font-size: 14px; min-width: 64px; }
/* 探测方式行（http 路径/协议/端口、tcp 端口、exec 命令），默认尺寸控件 */
.probe-target { display: flex; align-items: flex-end; gap: 12px; margin-bottom: 12px; }
.pt-item { display: flex; flex-direction: column; gap: 6px; }
.pt-label { font-size: 13px; color: #909399; }
/* 阈值网格：启动延迟/间隔/超时/失败阈值（readiness 加成功阈值自动换行） */
.probe-thresholds { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px 16px; max-width: 860px; margin-bottom: 4px; }
.probe-desc { font-size: 12px; margin-top: 8px; }
/* 端口表：72% 抽屉下限宽收敛，避免唯一的 min-width 弹性列（名称）被撑到巨宽 */
.ports-table { max-width: 880px; }
.svc-ports-table { max-width: 1040px; }
/* 存储挂载：引用对象下拉 + 新建按钮同行 */
.vol-ref { display: flex; align-items: center; gap: 6px; }
.vol-ref .el-select { flex: 1; min-width: 0; }

/* 白屏创建弹窗：内容多时窗口内滚动，表单项双列排布 */
.cfg-create-dlg .el-dialog__body { max-height: calc(100vh - 220px); overflow-y: auto; padding-top: 12px; }
.create-grid { display: grid; grid-template-columns: 1fr 1fr; column-gap: 24px; }
.ct-name { font-weight: 600; font-size: 15px; color: var(--el-color-primary); margin-bottom: 14px; }
/* 无编辑权限的表单模块：整体灰显且不可交互（后端仍兜底拦截） */
.form-readonly { pointer-events: none; opacity: .55; }
.img-hist-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.hist-img { line-height: 1.9; }
.hist-img code { font-family: ui-monospace, Consolas, monospace; word-break: break-all; }
/* 入口/命令编辑 */
.cmd-block { display: flex; flex-direction: column; gap: 14px; }
.cmd-row { display: flex; flex-direction: column; gap: 6px; }
.cmd-label { font-size: 13px; color: var(--el-text-color-secondary); }
.cmd-tip { font-size: 12px; }
/* Grafana 图标按钮（nodes 列表/概要在用）：与 el-button link 同基线对齐 */
.grafana-btn {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 4px 6px; margin: 0 2px; cursor: pointer; background: none; border: 0;
  border-radius: 4px; vertical-align: middle; line-height: 1;
  transition: background .15s;
}
.grafana-btn:hover { background: var(--el-fill-color-light); }
.grafana-btn:active { background: var(--el-fill-color); }
.grafana-btn img { display: block; width: 16px; height: 16px; }
/* 操作列「全息光线」按钮：悬浮时底部展开一道同色渐变光线（holographic underline）+
   极淡同色底 + 轻微上浮。不动文字（无加粗/发光），光线用 transform 展开不触发布局；
   光色随按钮类型取 Element Plus 色板变量，暗色主题自动适配；prefers-reduced-motion 仅去动效。 */
.ops-col .el-button {
  position: relative;
  padding: 3px 6px; margin-left: 0; margin-right: 4px;
  border-radius: 4px;
  transition: color .2s ease, background-color .2s ease, transform .2s ease;
}
.ops-col .el-button:last-child { margin-right: 0; }
/* 底部光线：两端透明的同色渐变线，悬浮时从中间向两边展开 */
.ops-col .el-button:not(.is-loading)::before {
  content: ''; position: absolute; left: 12%; right: 12%; bottom: -1px; height: 2px;
  border-radius: 2px; pointer-events: none;
  background: linear-gradient(90deg, transparent, var(--glow, var(--el-color-primary)), transparent);
  box-shadow: 0 0 6px var(--glow, var(--el-color-primary-light-5));
  transform: scaleX(0); opacity: 0;
  transition: transform .25s ease, opacity .25s ease;
}
.ops-col .el-button--success:not(.is-loading)::before { --glow: var(--el-color-success); }
.ops-col .el-button--warning:not(.is-loading)::before { --glow: var(--el-color-warning); }
.ops-col .el-button--danger:not(.is-loading)::before { --glow: var(--el-color-danger); }
.ops-col .el-button:hover:not(.is-disabled) {
  background: var(--el-fill-color-light);
  transform: translateY(-1px);
}
.ops-col .el-button:hover:not(.is-disabled)::before {
  transform: scaleX(1); opacity: 1;
}
@media (prefers-reduced-motion: reduce) {
  .ops-col .el-button:hover:not(.is-disabled) { transform: none; }
  .ops-col .el-button:not(.is-loading)::before { transition: opacity .2s ease; }
}

/* ========== 特效体系（参考设计稿：状态脉冲 / 终端辉光 / 删除警示 / Tab 弹性） ========== */
/* Pod 状态呼吸灯（Status Pulse FX）：Running 慢速呼吸光晕；异常高频涟漪扩散 */
.pod-pulse {
  display: inline-block; width: 8px; height: 8px;
  margin-right: 6px; vertical-align: middle;
  border-radius: 50%; background: #52c41a;
  animation: pulse-green 2s infinite cubic-bezier(.66, 0, 0, 1);
}
.pod-pulse.pulse-bad { background: #ff4d4f; animation: pulse-red .9s infinite cubic-bezier(.66, 0, 0, 1); }
@keyframes pulse-green { to { box-shadow: 0 0 0 9px rgba(82, 196, 26, 0); } }
@keyframes pulse-red { to { box-shadow: 0 0 0 9px rgba(255, 77, 79, 0); } }

/* 容器状态点：一容器一枚，绿呼吸 / 红涟漪 / 灰静态。
   方块形（微圆角 2px）：容器即「盒子」，比圆点更形象；同时与整 Pod 兜底灯（圆形）区分开——
   圆 = Pod 级，方 = 容器级 */
.cdots { display: inline-flex; align-items: center; gap: 3px; margin-right: 6px; vertical-align: middle; }
.cdot { width: 8px; height: 8px; border-radius: 2px; }
.cdot-ok { background: #52c41a; animation: pulse-green 2s infinite cubic-bezier(.66, 0, 0, 1); }
.cdot-bad { background: #ff4d4f; animation: pulse-red .9s infinite cubic-bezier(.66, 0, 0, 1); }
.cdot-dim { background: var(--el-text-color-placeholder); }

/* 终端按钮「>_」标记：等宽字体前缀（悬浮终端按钮时随按钮一起高亮，不闪烁） */
.term-cursor {
  font-family: ui-monospace, Consolas, monospace; font-size: 11px; margin-right: 2px;
}

/* 删除按钮「高危警示」：悬浮亮红淡底 + 微幅抖动（设计稿 #3，幅度收到 1px 防表格晃眼） */
.ops-col .el-button--danger:not(.is-disabled):hover {
  background: var(--el-color-danger-light-9);
  animation: danger-shake .3s ease-in-out;
}
@keyframes danger-shake {
  0%, 100% { transform: translateX(0); }
  25% { transform: translateX(-1px); }
  75% { transform: translateX(1px); }
}
@media (prefers-reduced-motion: reduce) {
  .pod-pulse { animation: none; }
  .cdot-ok, .cdot-bad { animation: none; }
  .ops-col .el-button--danger:not(.is-disabled):hover { animation: none; }
}
.pods-toolbar { display: flex; align-items: center; margin-bottom: 8px; }
/* Ingress 路由行：host 蓝 / path 绿 / 后端灰，三段对比便于扫读 */
.ing-row { display: flex; gap: 10px; font-size: 12px; line-height: 20px; align-items: baseline; white-space: nowrap; }
.ing-host { color: #409eff; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.ing-path { color: #67c23a; font-weight: 500; }
.ing-dim { color: var(--el-text-color-secondary); }
.res-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px 20px; max-width: 780px; }
.res-item { display: flex; flex-direction: column; gap: 6px; }
.res-item span { font-size: 13px; color: #909399; }
/* 资源量：数值 + 单位下拉 */
.qty-row { display: flex; gap: 6px; width: 100%; }
.qty-row .el-input { flex: 1; min-width: 0; }
.qty-row .qty-unit { width: 96px; flex-shrink: 0; }
.deploy-editor { width: 100%; height: 380px; border: 1px solid #dcdfe6; }
.ws-dialog-body { height: 65vh; }
/* Pod 列表「IP / 节点」列：两行紧凑展示，IP 等宽字体、点击复制 */
.pod-ip {
  font-family: ui-monospace, Consolas, monospace; font-size: 12px;
  cursor: pointer; line-height: 1.4;
}
.pod-node {
  font-size: 12px; color: var(--el-text-color-secondary);
  cursor: pointer; line-height: 1.4; margin-top: 2px;
}
.pod-ip:hover, .pod-node:hover { color: var(--el-color-primary); }
/* Node 列表名称列：IP 主行 + 原主机名 dim 副行（命名不规范历史遗留，主机名超长截断省略） */
.node-name-cell { line-height: 1.4; }
.node-name-cell .el-link { font-family: ui-monospace, Consolas, monospace; }
.node-host {
  font-size: 11px; margin-top: 1px;
  max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* 对话框标题行：标题 + 多容器切换下拉（终端/日志共用） */
.ws-dialog-header {
  display: flex; align-items: center; gap: 16px;
  padding-right: 24px; /* 避开右上角关闭按钮 */
}
.dlg-title { font-size: 16px; font-weight: 600; color: var(--el-text-color-primary); }
/* 白屏部署 */
.fd-form { max-height: 70vh; overflow: auto; padding-right: 8px; }
.fd-section { background: #fafbfd; border: 1px solid #ebeef5; border-radius: 8px; padding: 16px 16px 2px; margin-bottom: 14px; }
.fd-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 0 18px; }
.fd-grid-main { grid-template-columns: 240px 1fr; } /* 容器名窄列 + 镜像占满剩余宽度 */
.fd-table { width: 100%; }
.fd-ct { border: 1px solid #dcdfe6; border-radius: 8px; margin-bottom: 14px; overflow: hidden; background: #fff; }
.fd-ct-head { display: flex; align-items: center; justify-content: space-between; padding: 10px 16px; background: #f5f7fa; border-bottom: 1px solid #ebeef5; }
.fd-ct-title { font-weight: 600; color: var(--el-color-primary); font-size: 14px; }
.fd-ct-body { padding: 14px 16px 4px; }
.fd-res { width: 100%; }
.fd-res-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 18px; }
.resource-validation-error { margin-top: 8px; color: var(--el-color-danger); font-size: 12px; line-height: 1.5; }
.fd-field { display: flex; flex-direction: column; gap: 4px; }
.fd-field .dim { margin-left: 0; }
.fd-probe { display: flex; flex-direction: column; gap: 10px; width: 100%; }
.fd-probe-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.fd-advanced-entry {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin: 8px 0 6px;
  padding: 12px 14px;
  border: 1px solid #F1D6D8;
  border-left: 3px solid #D71920;
  border-radius: 8px;
  background: #FFFAFA;
}
.fd-advanced-copy { display: flex; flex-direction: column; gap: 3px; }
.fd-advanced-copy strong { color: #30343A; font-size: 13px; }
.fd-advanced-copy span { color: #777E88; font-size: 12px; }
.fd-advanced-control { display: flex; align-items: center; gap: 10px; flex-shrink: 0; color: #606772; font-size: 12px; }
.fd-advanced-switch { --el-switch-on-color: #D71920; }
.fd-advanced-info { margin-bottom: 12px; }
/* 折叠组（容器高级配置 / 高级选项）：透明背景融入卡片，标题行两端对齐 */
.fd-collapse { border: none; margin: 2px 0 8px; }
.fd-collapse :deep(.el-collapse-item__header) { height: 42px; line-height: 42px; background: #F8F9FB; border: 1px solid #E8EBEF; border-radius: 7px; font-weight: 600; padding: 0 10px; }
.fd-collapse :deep(.el-collapse-item.is-active .el-collapse-item__header) { color: #B70F18; border-color: #F1D6D8; border-radius: 7px 7px 0 0; }
.fd-collapse :deep(.el-collapse-item__wrap) { background: transparent; border: 1px solid #F1D6D8; border-top: none; border-radius: 0 0 7px 7px; }
.fd-collapse :deep(.el-collapse-item__content) { padding-bottom: 10px; }
.fd-col-title { flex: 1; display: flex; align-items: center; justify-content: space-between; padding-right: 10px; min-width: 0; }
.fd-col-title > span:first-child { font-size: 13px; }
.fd-col-sum { color: var(--el-text-color-secondary); font-size: 12px; font-weight: 400; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin-left: 12px; }
.fd-preview { height: 60vh; border: 1px solid #dcdfe6; border-radius: 6px; overflow: hidden; }
/* AI 修改 diff 确认视图：等宽字体逐行渲染，增/删行着色（改动一眼可见，敏感行核对靠人） */
.ai-diff { max-height: 56vh; overflow: auto; border: 1px solid #dcdfe6; border-radius: 6px; padding: 8px 0; background: #fafafa; font-family: 'Cascadia Mono', Consolas, monospace; font-size: 12px; line-height: 1.6; }
.ai-diff-line { white-space: pre-wrap; word-break: break-all; padding: 0 12px; }
.ai-diff-mark { display: inline-block; width: 14px; color: #909399; }
.ai-diff-add { background: #e6ffec; color: #116329; }
.ai-diff-del { background: #ffebe9; color: #82071e; text-decoration: line-through; }
/* 排空执行动效：卡片流 + 逐 Pod 翻绿（scale+fade pop），失败标红 */
.drain-run-head { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; }
.drain-run-progress { display: flex; align-items: center; gap: 10px; flex: 1; min-width: 0; }
.drain-run-count { font-variant-numeric: tabular-nums; font-size: 13px; white-space: nowrap; }
.drain-run-fail { color: var(--el-color-danger); }
.drain-cards { display: flex; flex-wrap: wrap; gap: 8px; max-height: 300px; overflow: auto; padding: 2px; }
.drain-card {
  display: flex; align-items: center; gap: 8px; padding: 6px 10px;
  border: 1px solid #dcdfe6; border-radius: 8px; background: #fff; max-width: 100%;
}
.drain-card-icon { font-size: 18px; color: var(--el-text-color-placeholder); }
.drain-card-name { font-size: 12px; line-height: 1.4; max-width: 240px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.drain-card-state { font-size: 11px; color: var(--el-text-color-secondary); line-height: 1.4; }
.drain-card.is-running { border-color: var(--el-color-primary-light-5); background: var(--el-color-primary-light-9); }
.drain-card.is-running .drain-card-icon { color: var(--el-color-primary); animation: drain-spin 1s linear infinite; }
.drain-card.is-done { border-color: #b3e19d; background: #f0f9eb; }
.drain-card.is-done .drain-card-icon { color: var(--el-color-success); }
.drain-card.is-failed { border-color: #f3b6b9; background: #fef0f0; }
.drain-card.is-failed .drain-card-icon { color: var(--el-color-danger); }
.drain-card.is-failed .drain-card-state { color: var(--el-color-danger); }
.drain-card.is-cancelled { border-color: #d3d4d6; background: #f4f4f5; }
.drain-card.is-cancelled .drain-card-icon { color: var(--el-text-color-secondary); }
/* 污点键单元格：输入框 + 系统标记并排 */
.taint-key-cell { display: flex; align-items: center; gap: 6px; }
.taint-key-cell .el-tag { flex-shrink: 0; }
@keyframes drain-spin { from { transform: rotate(0); } to { transform: rotate(360deg); } }
.drain-card-enter-active { transition: all 0.35s cubic-bezier(0.34, 1.56, 0.64, 1); }
.drain-card-enter-from { opacity: 0; transform: scale(0.6); }
.drain-card-leave-active { display: none; } /* 卡片不消失（列表恒定），仅状态翻转 */
@media (max-width: 760px) {
  .fd-advanced-entry { align-items: flex-start; flex-direction: column; }
  .fd-advanced-control { width: 100%; justify-content: space-between; }
}
</style>
