// 表单编辑（resedit）E2E：授权动作与表单 section 一一对应
// 运行: KUBEYARD_ADMIN_PW=<管理员密码> node e2e_form.mjs
const BASE = 'http://127.0.0.1:8080/api/v1'
const ADMIN_PW = process.env.KUBEYARD_ADMIN_PW
if (!ADMIN_PW) { console.error('缺少 KUBEYARD_ADMIN_PW 环境变量'); process.exit(2) }

async function api(method, path, body, token) {
  const r = await fetch(BASE + path, {
    method,
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: 'Bearer ' + token } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  })
  let data = null
  try { data = await r.json() } catch {}
  return { status: r.status, data }
}

const results = []
function check(name, cond, extra) {
  results.push({ name, pass: cond })
  console.log((cond ? 'PASS' : 'FAIL') + ' | ' + name + (cond ? '' : ' | ' + JSON.stringify(extra)?.slice(0, 300)))
}

async function login(u, p) {
  const r = await api('POST', '/auth/login', { username: u, password: p })
  return r.data?.access_token
}

const admin = await login('admin', ADMIN_PW)
check('admin 登录', !!admin)

const clusters = (await api('GET', '/clusters', null, admin)).data
const cl = clusters[0]
const depList = (await api('GET', `/clusters/${cl.name}/resources?group=apps&version=v1&resource=deployments`, null, admin)).data
const dep = depList?.items?.[0]
check('找到 deployment', !!dep, depList)
const ns = dep.metadata.namespace, name = dep.metadata.name
console.log(`目标: ${cl.name} ${ns}/${name}`)

// 找一个 service（同集群任意 ns）
const svcList = (await api('GET', `/clusters/${cl.name}/resources?version=v1&resource=services&namespace=${ns}`, null, admin)).data
const svc = svcList?.items?.[0]
console.log(`Service: ${svc ? svc.metadata.name : '（本 ns 无 service，跳过 service 用例）'}`)

// 建用户 + 授权：deployments 仅 image；services 仅 ports
await api('POST', '/admin/users', { username: 'ftest2', password: 'Ft@12345', display_name: '表单编辑测试', role: 'Viewer' }, admin)
const users = (await api('GET', '/admin/users', null, admin)).data
const fu = users.find((u) => u.username === 'ftest2')
check('用户创建', !!fu)

async function putGrants(grants) {
  const r = await api('PUT', `/admin/users/${fu.id}/grants`, { grants }, admin)
  check('授权保存 ' + grants.map((g) => `${g.resource}:${g.actions}`).join(','), r.status === 200, r)
}

const base = { cluster_id: cl.id, namespace: ns }
await putGrants([
  { ...base, resource: 'deployments', verb: 'get,list,update', actions: 'image' },
  ...(svc ? [{ ...base, resource: 'services', verb: 'get,list,update', actions: 'ports' }] : []),
])

let ft = await login('ftest2', 'Ft@12345')
check('ftest2 登录', !!ft)

const depObj = (await api('GET', `/clusters/${cl.name}/resource?group=apps&version=v1&resource=deployments&namespace=${ns}&name=${name}`, null, ft)).data
const ct0 = depObj.spec.template.spec.containers[0]

function edit(section, payload, token, resource = 'deployments', group = 'apps', tns = ns, tname = name) {
  return api('PUT', `/clusters/${cl.name}/resedit`, {
    group, version: 'v1', resource, namespace: tns, name: tname, section, payload,
  }, token)
}

// A: image（已授权，同值提交）→ 200
const a = await edit('image', { containers: depObj.spec.template.spec.containers.map((c) => ({ name: c.name, image: c.image })) }, ft)
check('A: image 授权用户保存(200)', a.status === 200, a)

// B/C: env / resources（未授权）→ 403
const b = await edit('env', { containers: [{ name: ct0.name, env: ct0.env || [] }] }, ft)
check('B: 未授权 env 被拒(403)', b.status === 403 && /env/.test(b.data?.message || ''), b)
const c = await edit('resources', { containers: [{ name: ct0.name, resources: ct0.resources || {} }] }, ft)
check('C: 未授权 resources 被拒(403)', c.status === 403, c)

// D: 扩权 image,ports,resources,env → 各 section 同值保存 200
await putGrants([
  { ...base, resource: 'deployments', verb: 'get,list,update', actions: 'image,ports,resources,env' },
  ...(svc ? [{ ...base, resource: 'services', verb: 'get,list,update', actions: 'ports' }] : []),
])
ft = await login('ftest2', 'Ft@12345') // 授权变更吊销旧 token

const d2 = await edit('ports', { containers: depObj.spec.template.spec.containers.map((c) => ({
  name: c.name, ports: (c.ports || []).map((p) => ({ name: p.name, containerPort: p.containerPort, protocol: p.protocol })),
})) }, ft)
check('D1: ports 同值保存(200)', d2.status === 200, d2)
const d3 = await edit('resources', { containers: depObj.spec.template.spec.containers.map((c) => ({ name: c.name, resources: c.resources || {} })) }, ft)
check('D2: resources 同值保存(200)', d3.status === 200, d3)
const d4 = await edit('env', { containers: depObj.spec.template.spec.containers.map((c) => ({ name: c.name, env: c.env || [] })) }, ft)
check('D3: env 同值保存(200)', d4.status === 200, d4)

// E: service ports 授权 → 200；selector 未授权 → 403
if (svc) {
  const sname = svc.metadata.name
  const e1 = await edit('ports', { ports: (svc.spec.ports || []).map((p) => ({ name: p.name, port: p.port, targetPort: p.targetPort, protocol: p.protocol, nodePort: p.nodePort })) }, ft, 'services', '', ns, sname)
  check('E1: service ports 同值保存(200)', e1.status === 200, e1)
  const e2 = await edit('selector', { selector: svc.spec.selector || {} }, ft, 'services', '', ns, sname)
  check('E2: service selector 未授权被拒(403)', e2.status === 403, e2)
}

// 清理
const del = await api('DELETE', `/admin/users/${fu.id}`, null, admin)
check('清理用户', del.status === 200, del)

const fails = results.filter((r) => !r.pass).length
console.log(`\n${results.length - fails}/${results.length} passed`)
process.exit(fails ? 1 : 0)
