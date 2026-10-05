/**
 * 开发用 mock 服务（P7 完成后即可删除）。
 *
 * 存在的理由：后端 HTTP 接口在 P7 才实现，
 * 但前端界面需要真实数据才能验证布局与交互。
 * 这里用 vite 的中间件插件拦截 /api，返回演示数据。
 */
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

const N = 24
const now = Date.now()
const rnd = (seed) => { let x = Math.sin(seed) * 10000; return x - Math.floor(x) }

const mkNode = (i) => ({
  id: i + 1, uid: `node-${i + 1}`,
  name: ['香港节点 01', '香港节点 02', '日本东京 01', '新加坡 01', '美国洛杉矶 01',
         '德国法兰克福 01', '韩国首尔 01', '台湾台北 01'][i % 8] + (i > 7 ? ` (${Math.floor(i/8)+1})` : ''),
  status: i % 7 === 3 ? 'offline' : i % 11 === 5 ? 'pending' : 'online',
  is_public: i % 3 === 0,
  cpu_cores: [2,4,8,16][i%4], mem_total: [2,4,8,16,32][i%5] * 1024**3,
  cpu_model: ['Intel Xeon E5-2680v4','AMD EPYC 7543','Intel Xeon Silver 4210','AMD Ryzen 9 5950X'][i%4],
  os_type: 'linux', os_version: 'Ubuntu 22.04.3 LTS', arch: 'x86_64',
  agent_version: '1.0.0', hostname: `web-${String(i+1).padStart(2,'0')}`,
  public_ip: `203.0.${113 + Math.floor(i/250)}.${10+i}`,
  geo_country: ['HK','JP','SG','US','DE','KR','TW'][i%7],
  last_seen_at: new Date(now - rnd(i)*3600000).toISOString(),
  disk_info: [{ mount:'/', fstype:'ext4', total: 80*1024**3 }, { mount:'/data', fstype:'xfs', total: 320*1024**3 }],
})

const mkSnapshot = (i) => ({
  collected_at: new Date().toISOString(),
  cpu_usage: 8 + rnd(i)*70,
  mem_total: [2,4,8,16,32][i%5] * 1024**3,
  mem_used: (1 + rnd(i+7)*7) * 1024**3,
  mem_usage: 20 + rnd(i+3)*70,
  load1: rnd(i+11)*4,
  uptime: 86400 * (3 + Math.floor(rnd(i+5)*90)),
  disks: [{ mount:'/', fstype:'ext4', total:80*1024**3, used: 30*1024**3, usage: 38+rnd(i)*50 },
          { mount:'/data', fstype:'xfs', total:320*1024**3, used: 200*1024**3, usage: 60+rnd(i+2)*35 }],
  net_io: [{ iface:'eth0', rx_bps: Math.floor(rnd(i+13)*5e7), tx_bps: Math.floor(rnd(i+17)*2e7),
             rx_total: 8e11, tx_total: 3e11 }],
})

const mkPoints = (seed, hours) => {
  const out = []; const step = 60
  const n = (hours*60)/step
  for (let k=0;k<n;k++){
    const t = Math.floor((now - (n-k)*step*1000)/1000)
    out.push({ t, cpu: 10+Math.abs(Math.sin((k+seed)*0.31))*75,
      mem: 30+Math.abs(Math.sin((k+seed)*0.17))*55,
      net_rx: Math.abs(Math.sin((k+seed)*0.4))*3e7,
      net_tx: Math.abs(Math.cos((k+seed)*0.33))*1.2e7 })
  }
  return out
}

const mkMonitor = (i) => ({
  id: i+1,
  name: ['官网首页','API 网关','对象存储','CDN 节点','登录服务','支付回调','文档站','状态页'][i%8],
  type: ['http','http','tcp','dns','ssl'][i%5],
  target: ['https://example.com','https://api.example.com/v1/health','example.com:22','example.com','example.com','https://pay.example.com/notify','https://docs.example.com','https://status.example.com'][i%8],
  config: i%5===3 ? { record:'A', expect_ips:['203.0.113.10'] } : { method:'GET', expect_status:[200] },
  interval_sec: 60, timeout_sec: 10, status: i%9===4?'down':i%13===6?'paused':'up',
  is_public: i%2===0,
  uptime_30d: i%9===4 ? 96.42 : i%13===6 ? null : 99.2+rnd(i)*0.79,
  avg_latency_ms: 40 + Math.floor(rnd(i+23)*400),
  last_checked_at: new Date(now - Math.floor(rnd(i+29)*60000)).toISOString(),
  sort: i,
})

const mkResults = (mid) => Array.from({length:60},(_,k)=>{
  const ok = k % 17 !== 3
  return { id: k+1, monitor_id: mid,
    checked_at: new Date(now - (60-k)*60000).toISOString(),
    ok, reason: ok?'ok':['timeout','conn_refused','status_mismatch','dns_error'][k%4],
    status_code: ok?200:502, latency_ms: ok?40+Math.floor(rnd(k+mid)*400):0,
    error_detail: ok?'': '连接超时（超过 10s）' }
})

function mockRoutes(req, res, next) {
  const p = req.url.split('?')[0]
  const json = (d, code=200) => { res.statusCode=code; res.setHeader('Content-Type','application/json'); res.end(JSON.stringify({code:0,data:d})) }

  if (p === '/api/auth/login') return json({ token:'mock-token', user:{ id:1, username:'admin', role:'owner', status:'active', created_at:new Date().toISOString() } })
  if (p === '/api/auth/me') return json({ id:1, username:'admin', role:'owner', status:'active', created_at:new Date().toISOString() })
  if (p === '/api/groups') return json([{id:1,name:'香港'},{id:2,name:'日本'},{id:3,name:'美洲'}])

  if (p.startsWith('/api/nodes/metrics/latest'))
    return json(Array.from({length:N},(_,i)=>({ node_id:i+1, ...mkSnapshot(i) })))
  if (p === '/api/nodes')
    return json({ items: Array.from({length:N},(_,i)=>mkNode(i)), total:N })
  if (p.startsWith('/api/nodes/') && p.endsWith('/metrics')) {
    const uid = p.split('/')[3]
    const i = Math.max(0, N-1)
    return json({ items: mkPoints(i, Number(new URL(req.url,'http://x').searchParams.get('range')?.replace('h','')||1)) })
  }
  if (p.match(/^\/api\/nodes\/[^/]+$/)) return json(mkNode(0))
  if (p === '/api/nodes/new') return json({ id:99, uid:'node-99', secret:'ZmFrZS1zZWNyZXQtZm9yLWRlbW8tb25seS0zMmJ5dGVz', install_command:'curl -fsSL https://example.com/agent.sh | sh -s -- --server probe.example.com:8008 --uuid node-99 --secret ZmFrZS1zZWNyZXQ=' })

  if (p.startsWith('/api/monitors/') && p.endsWith('/stats'))
    return json({ total:1440, up:1428, latency_p50:120, latency_p95:340, latency_p99:580, uptime_percent:99.17, sample_insufficient:false })
  if (p.match(/^\/api\/monitors\/\d+\/results$/))
    return json({ items: mkResults(1) })
  if (p.match(/^\/api\/monitors\/\d+\/certificate$/))
    return json({ subject:'CN=example.com', issuer:"Let's Encrypt R3", not_after:new Date(now+76*86400e3).toISOString(), days_left:76, fingerprint:'a3f9c2b1d4e5f678' })
  if (p.match(/^\/api\/monitors\/\d+$/)) return json(mkMonitor(0))
  if (p === '/api/monitors') return json({ items: Array.from({length:16},(_,i)=>mkMonitor(i)), total:16 })

  if (p === '/api/alerts') return json({ items: Array.from({length:20},(_,i)=>({
      id:i+1, target_type: i%3===0?'node':i%3===1?'monitor':'cert',
      target_name: mkNode(i%N).name, severity: i%5===0?'critical':i%3===0?'warning':'info',
      status: i%4===0?'resolved':i%7===0?'acked':'firing',
      message: i%3===0 ? '目标「香港节点 01」的 cpu_usage 当前为 92.40，触发规则「CPU 持续过高」（> 90）'
        : i%3===1 ? '目标「API 网关」状态码 502 不在期望范围 [200]' : '证书将在 30 天内过期（剩余 28 天，颁发者 Let\'s Encrypt R3）',
      notified: i%4!==0,
      first_fired_at: new Date(now - i*3600000).toISOString(),
      last_fired_at: new Date(now - i*600000).toISOString(),
    })), total:20 })
  if (p === '/api/alert-rules') return json([
      { id:1, name:'CPU 持续过高', target_type:'node', target_id:null, metric:'cpu_usage',
        condition:{op:'>',value:90,for_times:3,for_minutes:2}, severity:'warning',
        channel_ids:[1,2], dedup_window_sec:1800, enabled:true, created_at:new Date().toISOString() },
      { id:2, name:'内存告急', target_type:'node', target_id:null, metric:'mem_usage',
        condition:{op:'>',value:92,for_times:5,for_minutes:5}, severity:'critical',
        channel_ids:[1], dedup_window_sec:600, enabled:true, created_at:new Date().toISOString() }])
  if (p === '/api/channels') return json([
      { id:1, name:'运维群', type:'wecom', config:{ key:'a1b2c3d4-e5f6' }, enabled:true, created_at:new Date().toISOString() },
      { id:2, name:'值班邮箱', type:'email', config:{ host:'smtp.example.com', port:465, from:'probe@example.com', to:'ops@example.com', tls:true }, enabled:true, created_at:new Date().toISOString() },
      { id:3, name:'Telegram', type:'telegram', config:{ bot_token:'123456:ABC-DEF', chat_id:'-1001234567890' }, enabled:false, created_at:new Date().toISOString() }])
  if (p === '/api/visibility') return json([
      ...['name','cpu_model','cores_logical','mem_total','disk_info','os_type','os_version','cpu_usage','mem_usage','uptime_30d','is_online','geo_country']
        .map(f=>({id:Math.random(), scope:'public_status', field:f, visible:true, mask_mode:'full', mask_rule:null})),
      ...['public_ip','geo_city','hostname','fqdn','remark']
        .map(f=>({id:Math.random(), scope:'public_status', field:f, visible:false,
          mask_mode: f==='public_ip'?'partial':'hide', mask_rule: f==='public_ip'?'country_only':null})),
      {id:9001, scope:'public_status', field:'agent_secret', visible:false, mask_mode:'hide', mask_rule:null},
      {id:9002, scope:'public_status', field:'internal_ip', visible:false, mask_mode:'hide', mask_rule:null},
      {id:9003, scope:'public_status', field:'mac_address', visible:false, mask_mode:'hide', mask_rule:null},
      {id:9004, scope:'api_unauth', field:'public_ip', visible:false, mask_mode:'hide', mask_rule:null},
      {id:9005, scope:'export', field:'public_ip', visible:true, mask_mode:'partial', mask_rule:'first_two_octets'},
    ])
  if (p === '/api/users') return json([
      {id:1,username:'admin',role:'owner',status:'active',created_at:'2026-06-01T10:00:00Z'},
      {id:2,username:'ops',role:'admin',status:'active',created_at:'2026-07-15T03:20:00Z'},
      {id:3,username:'viewer',role:'viewer',status:'active',created_at:'2026-08-20T08:00:00Z'}])
  if (p === '/api/audit-logs') return json({ items: Array.from({length:30},(_,i)=>({
      id:i+1, username:['admin','ops'][i%2], action:['create_node','update_visibility_policy','delete_monitor','login','node.hardware_changed'][i%5],
      target_type:['node','monitor','visibility','-','node'][i%5],
      target_id:['node-1','7','public_ip','-','node-3'][i%5],
      ip:`10.0.${i%5}.${20+i}`, detail: i%5===4?{before:{cores:2,mem_gb:2},after:{cores:4,mem_gb:8}}:(i%3===0?{name:'香港节点 0'+(i+1),is_public:true}:null),
      created_at:new Date(now - i*7200000).toISOString() })), total:30 })
  if (p === '/api/status/monitors') return json(Array.from({length:6},(_,i)=>({
      id:i+1, name:['官网首页','API 网关','对象存储','CDN 节点','登录服务','文档站'][i],
      status: i===2?'down':'up', uptime_30d: i===2?98.71:99.9,
      avg_latency_ms: 60+i*40, last_checked_at:new Date(now-i*60000).toISOString() })))

  return next()
}

export default defineConfig({
  plugins: [vue(), { name:'mock-api', configureServer(s){ s.middlewares.use(mockRoutes) } }],
  base: './',
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: { port: 5173, host:'0.0.0.0' },
})
