// 启停 E2E：临时 ns + sleep 负载，验证 停止→副本0+注解 / 启动→副本恢复 / 权限拒绝
// 运行: KUBEYARD_ADMIN_PW=<管理员密码> node e2e_stop.mjs
const BASE = 'http://127.0.0.1:8080/api/v1'
const ADMIN_PW = process.env.KUBEYARD_ADMIN_PW
if (!ADMIN_PW) { console.error('缺少 KUBEYARD_ADMIN_PW 环境变量'); process.exit(2) }
const NS = 'kubeyard-e2e'
const DEP = 'kubeyard-e2e-stop'
const ANN = 'kubeyard.io/original-replicas'

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
async function login(u, p) {
  return (await api('POST', '/auth/login', { username: u, password: p })).data?.access_token
}
const results = []
function check(name, cond, extra) {
  results.push({ name, pass: cond })
  console.log((cond ? 'PASS' : 'FAIL') + ' | ' + name + (cond ? '' : ' | ' + JSON.stringify(extra)?.slice(0, 300)))
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const admin = await login('admin', ADMIN_PW)
check('admin 登录', !!admin)
const cl = (await api('GET', '/clusters', null, admin)).data[0]
console.log('集群:', cl.name)

// 借用集群内已有镜像（节点上大概率已存在），command 改为 sleep，不运行业务逻辑
const depList = (await api('GET', `/clusters/${cl.name}/resources?group=apps&version=v1&resource=deployments`, null, admin)).data
const image = depList.items[0].spec.template.spec.containers[0].image
console.log('测试镜像:', image)

// 1. 建临时 ns + deployment（2 副本）
const yamlDoc = `apiVersion: v1
kind: Namespace
metadata:
  name: ${NS}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${DEP}
  namespace: ${NS}
spec:
  replicas: 2
  selector:
    matchLabels: { app: ${DEP} }
  template:
    metadata:
      labels: { app: ${DEP} }
    spec:
      containers:
      - name: main
        image: ${image}
        command: ["sh", "-c", "sleep 3600"]
`
const apply = await api('POST', `/clusters/${cl.name}/apply`, { yaml: yamlDoc }, admin)
check('创建测试负载', apply.data?.ok, apply.data?.results)
await sleep(2000)

async function getDep(token) {
  return (await api('GET', `/clusters/${cl.name}/resource?group=apps&version=v1&resource=deployments&namespace=${NS}&name=${DEP}`, null, token)).data
}
function op(token, action) {
  return api('POST', `/clusters/${cl.name}/${action}`, {
    group: 'apps', version: 'v1', resource: 'deployments', namespace: NS, name: DEP,
  }, token)
}

// 2. 用户与授权
await api('POST', '/admin/users', { username: 'fstop', password: 'Ft@12345', role: 'Viewer' }, admin)
await api('POST', '/admin/users', { username: 'fnoact', password: 'Ft@12345', role: 'Viewer' }, admin)
const us = (await api('GET', '/admin/users', null, admin)).data
const uf = us.find((u) => u.username === 'fstop')
const un = us.find((u) => u.username === 'fnoact')
check('用户创建', !!uf && !!un)
await api('PUT', `/admin/users/${uf.id}/grants`, { grants: [{ cluster_id: cl.id, namespace: NS, resource: 'deployments', verb: 'get,list,update', actions: 'stop,start' }] }, admin)
await api('PUT', `/admin/users/${un.id}/grants`, { grants: [{ cluster_id: cl.id, namespace: NS, resource: 'deployments', verb: 'get,list,update', actions: 'image' }] }, admin)
let ft = await login('fstop', 'Ft@12345')
const fn = await login('fnoact', 'Ft@12345')
check('用户登录', !!ft && !!fn)

// 3. 无 stop/start 动作 → 403
const n1 = await op(fn, 'stop')
check('未授权 stop 被拒(403)', n1.status === 403, n1)
const n2 = await op(fn, 'start')
check('未授权 start 被拒(403)', n2.status === 403, n2)

// 4. fstop 停止 → 副本 0 + 注解记录 2
const s1 = await op(ft, 'stop')
check('停止(200)', s1.status === 200, s1)
let d = await getDep(ft)
check('停止后副本=0', d?.spec?.replicas === 0, d?.spec)
check('停止后注解记录原副本数=2', d?.metadata?.annotations?.[ANN] === '2', d?.metadata?.annotations)

// 5. 重复停止 → 400
const s2 = await op(ft, 'stop')
check('重复停止被拒(400)', s2.status === 400, s2)

// 6. 启动 → 副本恢复 2，注解清除
const s3 = await op(ft, 'start')
check('启动(200)', s3.status === 200, s3)
d = await getDep(ft)
check('启动后副本恢复=2', d?.spec?.replicas === 2, d?.spec)
check('启动后注解已清除', !d?.metadata?.annotations?.[ANN], d?.metadata?.annotations)

// 7. 重复启动 → 400
const s4 = await op(ft, 'start')
check('重复启动被拒(400)', s4.status === 400, s4)

// 8. 清理
await api('DELETE', `/admin/users/${uf.id}`, null, admin)
await api('DELETE', `/admin/users/${un.id}`, null, admin)
const del = await api('DELETE', `/clusters/${cl.name}/resource?version=v1&resource=namespaces&name=${NS}`, null, admin)
check('清理临时命名空间', del.status === 200, del)

const fails = results.filter((r) => !r.pass).length
console.log(`\n${results.length - fails}/${results.length} passed`)
process.exit(fails ? 1 : 0)
