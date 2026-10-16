// 字段级授权 E2E：image-only 用户改 env 应被拒，改/保持 image 应放行，scale/restart 应被拒
// 运行: KUBEYARD_ADMIN_PW=<管理员密码> node e2e_fields.mjs
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
  results.push({ name, pass: cond, extra })
  console.log((cond ? 'PASS' : 'FAIL') + ' | ' + name + (cond ? '' : ' | ' + JSON.stringify(extra)?.slice(0, 400)))
}

// 1. admin 登录
const login = await api('POST', '/auth/login', { username: 'admin', password: ADMIN_PW })
const admin = login.data?.access_token || login.data?.token
check('admin 登录', !!admin, login)

// 2. 找一个真实集群 + deployment
const clusters = (await api('GET', '/clusters', null, admin)).data
check('有已导入集群', Array.isArray(clusters) && clusters.length > 0, clusters)
const cl = clusters[0]
const depList = (await api('GET', `/clusters/${cl.name}/resources?group=apps&version=v1&resource=deployments`, null, admin)).data
const items = depList?.items || []
check('集群里有 deployment', items.length > 0, { count: items.length })
if (!items.length) { console.log(JSON.stringify(results)); process.exit(1) }
const dep = items[0]
const ns = dep.metadata.namespace, name = dep.metadata.name
console.log(`目标: ${cl.name} ${ns}/${name}`)

// 3. 建测试用户 + 授权（仅 deployments 的 get,list,update + 仅 image 字段）
await api('POST', '/admin/users', { username: 'ftest', password: 'Ft@12345', display_name: '字段权限测试', role: 'Viewer' }, admin)
const users = (await api('GET', '/admin/users', null, admin)).data
const fu = users.find(u => u.username === 'ftest')
check('测试用户已创建', !!fu, users.map(u => u.username))
const gr = await api('PUT', `/admin/users/${fu.id}/grants`, {
  grants: [{ cluster_id: cl.id, namespace: ns, resource: 'deployments', verb: 'get,list,update', actions: 'image' }]
}, admin)
check('授权保存', gr.status === 200, gr)
const grBack = (await api('GET', `/admin/users/${fu.id}/grants`, null, admin)).data
check('actions 回读一致', grBack?.[0]?.actions === 'image', grBack)

// 4. ftest 登录，取线上对象
const fl = await api('POST', '/auth/login', { username: 'ftest', password: 'Ft@12345' })
const ft = fl.data?.access_token || fl.data?.token
check('ftest 登录', !!ft, fl)
const cur = (await api('GET', `/clusters/${cl.name}/resource?group=apps&version=v1&resource=deployments&namespace=${ns}&name=${name}`, null, ft))
check('ftest 可读对象', cur.status === 200, cur)
const obj = cur.data

// 5. A: 改 env → 403
const withEnv = JSON.parse(JSON.stringify(obj))
withEnv.spec.template.spec.containers[0].env = [{ name: 'HACK', value: '1' }]
const a = await api('PUT', `/clusters/${cl.name}/resource?group=apps&version=v1&resource=deployments&namespace=${ns}&name=${name}`, withEnv, ft)
check('A: 改 env 被拒(403)', a.status === 403, a)
check('A: 报错指出 env 路径', /env/.test(a.data?.message || ''), a.data?.message)

// 6. B: 仅动 image（改成相同值）→ 200 放行
const sameImg = JSON.parse(JSON.stringify(obj))
const c0 = sameImg.spec.template.spec.containers[0]
c0.image = c0.image // 相同值，diff 为空
const b = await api('PUT', `/clusters/${cl.name}/resource?group=apps&version=v1&resource=deployments&namespace=${ns}&name=${name}`, sameImg, ft)
check('B: 无字段变更放行(200)', b.status === 200, b)

// 6b. B2: 真改 image 字段路径但值相同结构 → 提交一个 env 之外的第二个容器不动，仅确保 image 路径出现在提交里
// (跳过真实改镜像，避免影响集群)

// 7. C: scale → 403
const c = await api('POST', `/clusters/${cl.name}/scale`, { group: 'apps', version: 'v1', resource: 'deployments', namespace: ns, name, replicas: 1 }, ft)
check('C: scale 被拒(403)', c.status === 403, c)

// 8. D: restart → 403
const d = await api('POST', `/clusters/${cl.name}/restart`, { group: 'apps', version: 'v1', resource: 'deployments', namespace: ns, name }, ft)
check('D: restart 被拒(403)', d.status === 403, d)

// 9. 清理
const del = await api('DELETE', `/admin/users/${fu.id}`, null, admin)
check('清理测试用户', del.status === 200, del)

const fails = results.filter(r => !r.pass).length
console.log(`\n${results.length - fails}/${results.length} passed`)
process.exit(fails ? 1 : 0)
