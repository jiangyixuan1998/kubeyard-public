// SchedEditor 组件交互测试：真实挂载组件，验证数据流（选中/增删行/级联多选/读写回）。
// 背景：该组件四轮线上 bug 全是「编译/类型检查通过但运行时数据流断裂」——
// v-model 写 readonly props、模板引用未声明标识符、el-option :value 绑对象、
// getter/setter 对象语法错。唯一可靠的防线是真实挂载 + 断言 props 数据流。
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import SchedEditor, {
  schedModelDefault,
  schedToK8s,
  schedFromK8s,
  validateSched,
} from '../SchedEditor.vue'
import type { SchedModel } from '../SchedEditor.vue'

const base = (): SchedModel => schedModelDefault()

const nodes = [
  { name: 'node-a', unschedulable: false },
  { name: 'node-b', unschedulable: true },
]
const labelKeys = ['disktype', 'kubernetes.io/os']
const labelValues = { disktype: ['ssd', 'hdd'], 'kubernetes.io/os': ['linux'] }

function mountEditor(model: SchedModel = base()) {
  return mount(SchedEditor, {
    props: { modelValue: model, nodes, labelKeys, labelValues },
    global: { plugins: [ElementPlus] },
  })
}

describe('SchedEditor 数据流', () => {
  // helper：访问组件 setup 内部状态（m / setKv / setValuesOfRow 等）
  // test-utils 把 <script setup> 绑定挂在 vm.$ 的 setupState 上
  function setupState(w: any) {
    return (w.vm.$ as any).setupState
  }

  it('nodeName 写入非字符串（历史 bug 形态）会被 validateSched 拦截', () => {
    const m = base()
    ;(m as any).node_name = { name: 'x' }
    const err = validateSched(m)
    expect(err).toContain('数据异常')
  })

  it('setKv：「键=值」拆分为 key/value；无等号整串作键', async () => {
    const model = base()
    model.node_selector.push({ key: '', value: '' })
    const w = mountEditor(model)
    const row = setupState(w).m.node_selector[0]
    setupState(w).setKv(row, 'disktype=ssd')
    expect(row.key).toBe('disktype')
    expect(row.value).toBe('ssd')
    setupState(w).setKv(row, 'customkey')
    expect(row.key).toBe('customkey')
    expect(row.value).toBe('')
  })

  it('setValuesOfRow：数组回收为逗号串（曾因 getter/setter 语法错抛 only a getter）', async () => {
    const model = base()
    model.node_affinity_required.push({ key: 'disktype', operator: 'In', values: '' })
    const w = mountEditor(model)
    const row = setupState(w).m.node_affinity_required[0]
    setupState(w).setValuesOfRow(row, ['ssd', 'hdd'])
    expect(typeof row.values).toBe('string')
    expect(row.values).toBe('ssd,hdd')
    setupState(w).setValuesOfRow(row, 'a,b')
    expect(row.values).toBe('a,b')
  })

  it('本地副本变更 → emit 完整模型（深拷贝、含全部六个字段）', async () => {
    const w = mountEditor()
    const st = setupState(w)
    st.m.node_name = 'node-a'
    await w.vm.$nextTick()
    const emitted = w.emitted('update:modelValue')
    expect(emitted).toBeTruthy()
    const last = emitted![emitted!.length - 1][0] as SchedModel
    expect(last.node_name).toBe('node-a')
    expect(Object.keys(last).sort()).toEqual([
      'node_affinity_preferred', 'node_affinity_required', 'node_name',
      'node_selector', 'pod_anti_preferred', 'pod_anti_required',
    ])
  })

  it('添加条件按钮：行结构入列并 emit', async () => {
    const w = mountEditor()
    const btn = w.findAll('button').find((b) => b.text().includes('添加条件'))
    expect(btn).toBeTruthy()
    await btn!.trigger('click')
    await w.vm.$nextTick()
    const last = w.emitted('update:modelValue')!.slice(-1)[0][0] as SchedModel
    expect(last.node_selector).toHaveLength(1)
  })

  it('props 回流：父组件换新对象 → 本地副本同步（保存后读回场景）', async () => {
    const w = mountEditor()
    const fresh = base()
    fresh.node_name = 'node-b'
    await w.setProps({ modelValue: fresh })
    await w.vm.$nextTick()
    expect(setupState(w).m.node_name).toBe('node-b')
  })

  it('nodeName 选项渲染为字符串（防止 :value 绑对象回归）', () => {
    const w = mountEditor()
    // el-option 渲染后的 DOM 里 label 应为节点名，且选项组件的 value prop 是 string
    const opts = w.findAllComponents({ name: 'ElOption' })
    const nodeNameOpts = opts.filter((o) => typeof o.props('value') === 'string' && (nodes.map((n) => n.name) as any[]).includes(o.props('value')))
    expect(nodeNameOpts.length).toBe(nodes.length)
    for (const o of nodeNameOpts) {
      expect(o.props('label')).toBe(o.props('value'))
    }
  })

  it('nodeName 清空（el-select clearable 传 undefined）：不产生 undefined 字段（用户实测「移除节点定向保存无响应」）', async () => {
    const model = base()
    model.node_name = 'node-a'
    const w = mountEditor(model)
    await w.vm.$nextTick()
    const nodeNameSelect = w.findAllComponents({ name: 'ElSelect' })[0]
    // Element Plus clearable 清空时 emit undefined（非 ''）
    await (nodeNameSelect.vm as any).$emit('update:modelValue', undefined)
    await w.vm.$nextTick()
    const st = setupState(w)
    expect(st.m.node_name).toBe('') // 归一为空串
    // emit 出的模型也不含 undefined 键
    const emitted = w.emitted('update:modelValue')!
    const last = emitted[emitted.length - 1][0] as SchedModel
    expect('node_name' in last).toBe(true)
    expect(last.node_name).toBe('')
    // validateSched 不崩
    expect(validateSched(last)).toBe('')
  })

  it('props 回流含 undefined 字段（外部构造不完整模型）：归一不崩', async () => {
    const w = mountEditor()
    // 模拟脏数据：node_name 缺失
    await w.setProps({ modelValue: { node_selector: [] } as any })
    await w.vm.$nextTick()
    expect(setupState(w).m.node_name).toBe('')
    expect(setupState(w).m.node_selector).toEqual([])
  })

  it('亲和行「键=值」下拉：setKv 只碰 key/value，values 与 operator 保留', async () => {
    const model = base()
    model.node_affinity_required.push({ key: '', operator: 'In', values: 'ssd' })
    const w = mountEditor(model)
    const row = setupState(w).m.node_affinity_required[0]
    setupState(w).setKv(row, 'disktype=ssd')
    expect(row.key).toBe('disktype')
    expect(row.value).toBe('ssd')
    expect(row.operator).toBe('In')
    expect(row.values).toBe('ssd')
  })

  it('onOperatorChange：切到 Exists/DoesNotExist 清空残留值', () => {
    const w = mountEditor()
    const row = { key: 'disktype', operator: 'In', values: 'ssd,hdd' }
    setupState(w).onOperatorChange(row, 'Exists')
    expect(row.operator).toBe('Exists')
    expect(row.values).toBe('')
    setupState(w).onOperatorChange(row, 'In')
    expect(row.values).toBe('') // 保持空（用户重新填）
  })

  it('labelPairs：nodeSelector 与亲和下拉共用「键=值」候选', () => {
    const w = mountEditor()
    const pairs = setupState(w).labelPairs
    expect(pairs).toContain('disktype=ssd')
    expect(pairs).toContain('kubernetes.io/os=linux')
  })

  it('schedToK8s/schedFromK8s 往返无损（含 preferred 真实形状）', () => {
    const m = base()
    m.node_name = 'node-a'
    m.node_selector.push({ key: 'disktype', value: 'ssd' })
    m.node_affinity_required.push({ key: 'disktype', operator: 'In', values: 'ssd,hdd' })
    m.node_affinity_preferred.push({ key: 'disktype', operator: 'In', values: 'ssd', weight: 60 })
    m.pod_anti_required.push({ key: 'app', operator: 'In', values: 'web', topology_key: 'kubernetes.io/hostname' })
    const k8s = schedToK8s(m)
    expect(k8s.nodeName).toBe('node-a')
    expect(k8s.affinity!.nodeAffinity!.preferredDuringSchedulingIgnoredDuringExecution[0])
      .toEqual({ weight: 60, preference: { nodeSelectorTerms: [{ matchExpressions: [{ key: 'disktype', operator: 'In', values: ['ssd'] }] }] } })
    const ps = { nodeName: k8s.nodeName, nodeSelector: k8s.nodeSelector, affinity: k8s.affinity }
    const back = schedFromK8s(ps)
    expect(back.unmodeled).toBe(false)
    expect(back.model.node_name).toBe('node-a')
    expect(back.model.node_affinity_preferred[0].weight).toBe(60)
    expect(back.model.pod_anti_required[0].topology_key).toBe('kubernetes.io/hostname')
  })
})
