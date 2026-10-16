// WebSocket 通道：首帧 JSON 鉴权 → 二进制数据 / 文本控制
// 协议约定（与后端 ws/hub.go 一致）：
//   客户端首帧（文本）: {"type":"auth","token":"<access>"}
//   服务端回: {"type":"auth_ok"} 或 {"type":"auth_failed","message":...}
//   二进制帧 = 终端输入/输出、日志内容
//   文本帧   = 控制（resize/ping/end/error）

type ControlHandler = (msg: any) => void

export class WsChannel {
  private ws: WebSocket | null = null
  private encoder = new TextEncoder()
  private decoder = new TextDecoder()
  onBinary: ((data: string) => void) | null = null
  onControl: ControlHandler | null = null
  onOpen: (() => void) | null = null
  onClose: (() => void) | null = null

  connect(path: string) {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const token = localStorage.getItem('ky_access') || ''
    this.ws = new WebSocket(`${proto}://${location.host}${path}`)
    this.ws.binaryType = 'arraybuffer'
    this.ws.onopen = () => {
      // 首帧鉴权
      this.ws?.send(JSON.stringify({ type: 'auth', token }))
      this.onOpen?.()
    }
    this.ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        let msg: any
        try { msg = JSON.parse(ev.data) } catch { return }
        this.onControl?.(msg)
      } else {
        // stream: true —— pty 输出块可能在多字节字符（中文）中间截断，
        // 逐消息独立解码会把残缺字节变成 U+FFFD 乱码；流式解码把不完整的
        // 尾部字节留到下一帧拼齐
        this.onBinary?.(this.decoder.decode(ev.data, { stream: true }))
      }
    }
    this.ws.onclose = () => this.onClose?.()
    this.ws.onerror = () => { /* onclose 会跟随触发 */ }
  }

  // 终端输入必须以二进制帧发送
  sendBinary(text: string) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(this.encoder.encode(text))
    }
  }

  sendControl(obj: any) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(obj))
    }
  }

  close() {
    try { this.ws?.close() } catch { /* 忽略 */ }
    this.ws = null
  }
}
