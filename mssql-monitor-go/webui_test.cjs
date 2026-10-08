// Headless regression checks of the embedded browser script. No npm packages.
const {readFileSync} = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const script = readFileSync(__dirname + '/web/index.html','utf8').match(/<script>([\s\S]*?)<\/script>/)[1];
const turn = () => new Promise(resolve => setImmediate(resolve));
async function fixture(exitResult, confirm = true) {
  const nodes = new Map(), timers = new Set(), calls = [];
  const element = id => {
    if(!nodes.has(id)) nodes.set(id, {value:'', checked:false, disabled:false, style:{}, textContent:'', innerHTML:'', classList:{add(){},remove(){},toggle(){}}, click(){}, appendChild(){}, getContext(){return null;}});
    return nodes.get(id);
  };
  let timer = 0;
  const document = {body:{innerHTML:'original'}, getElementById:element,
    querySelectorAll:()=>[], querySelector:()=>element('query'), createElement:()=>element('created')};
  const ctx = vm.createContext({document, console, AbortController, setInterval:()=>{const id=++timer;timers.add(id);return id;}, clearInterval:id=>timers.delete(id),
    confirm:()=>confirm, alert(){}, window:{open(){}},
    fetch: async (path, options) => {
      calls.push(path);
      if(path==='/api/exit') {
        assert.equal(options.method,'POST');
        if(exitResult instanceof Error) throw exitResult;
        return {json:async()=>exitResult};
      }
      const data = path==='/api/state' ? {connected:false, config:{workload:'accounting'}} : {};
      return {json:async()=>data};
    }});
  vm.runInContext(script,ctx);
  await turn();
  assert.equal(element('s-workload').value,'accounting','state must populate workload selector');
  vm.runInContext('reportPoll = setInterval(pollReport, 1500)',ctx);
  return {ctx, document, nodes, timers, calls, element};
}
(async()=>{
  for(const result of [{ok:true}, {ok:false,error:'denied'}, new Error('network lost')]){
    const f=await fixture(result);
    f.element('b-exit').onclick();
    await turn();
    assert.equal(f.timers.size,0,'all polling timers must stop, including reportPoll');
    if(result.ok===true){
      assert.match(f.document.body.innerHTML,/Мониторинг и диагностика завершены/);
      const n=f.calls.length;
      vm.runInContext('refreshState();loadLog();pollReport()',f.ctx);
      await turn();assert.equal(f.calls.length,n,'no requests after confirmed exit');
    } else {
      assert.equal(f.document.body.innerHTML,'original','network error must not claim successful shutdown');
      assert.match(f.element('exit-status').textContent,/не подтверждено/);
      assert.equal(f.element('b-exit').disabled,false,'allow retry');
    }
  }
  const f=await fixture({ok:true},false);
  f.element('b-exit').onclick(); await turn();
  assert.equal(f.calls.includes('/api/exit'),false,'cancelled confirmation must not exit');
  assert.equal(f.timers.size,3);
  console.log('Browser script: 4 shutdown scenarios passed');
})().catch(e=>{console.error(e);process.exitCode=1});
