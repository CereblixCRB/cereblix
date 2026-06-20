
importScripts('wasm_exec.js');

let work = null;        
let nonce = '0';
let mining = false;
let ready = false;

const go = new Go();

fetch('cereblix.wasm')
  .then(r => r.arrayBuffer())
  .then(buf => WebAssembly.instantiate(buf, go.importObject))
  .then(res => {
    go.run(res.instance);   
    ready = true;
    postMessage({ type: 'ready' });
    if (work) loop();
  })
  .catch(err => postMessage({ type: 'err', err: String(err) }));

onmessage = (e) => {
  const m = e.data;
  if (m.type === 'work') {
    work = m.work;
    nonce = m.startNonce;
    if (ready && !mining) loop();
  } else if (m.type === 'stop') {
    work = null;
  }
};

function loop() {
  mining = true;
  const BATCH = 16; 
  function step() {
    if (!work) { mining = false; return; }
    let r;
    try {
      r = self.cereblixMine(work.header, work.target, work.seed, work.height, nonce, BATCH);
    } catch (err) {
      postMessage({ type: 'err', err: String(err) });
      mining = false;
      return;
    }
    if (r.err) { postMessage({ type: 'err', err: r.err }); mining = false; return; }
    if (r.found) {
      postMessage({ type: 'found', nonce: r.nonce, id: work.id });
      
      nonce = (BigInt(r.nonce) + 1n).toString();
    } else {
      nonce = r.next;
      postMessage({ type: 'progress', hashed: r.hashed });
    }
    setTimeout(step, 0); 
  }
  step();
}
