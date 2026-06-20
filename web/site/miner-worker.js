// Web Worker: loads the NeuroMorph WASM hasher and mines off the UI thread.
importScripts('wasm_exec.js');

let work = null;        // {id, header, target, seed, height}
let nonce = '0';
let mining = false;
let ready = false;

const go = new Go();
// arrayBuffer path (not instantiateStreaming) so it works regardless of the
// server's MIME type for .wasm.
fetch('cereblix.wasm')
  .then(r => r.arrayBuffer())
  .then(buf => WebAssembly.instantiate(buf, go.importObject))
  .then(res => {
    go.run(res.instance);   // runs main(), registers self.cereblixMine, then parks
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
  const BATCH = 16; // hashes per slice; we yield between slices to take new work
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
      // advance past the found nonce so we keep searching for more shares (pool
      // mode) instead of re-finding and re-submitting the same one
      nonce = (BigInt(r.nonce) + 1n).toString();
    } else {
      nonce = r.next;
      postMessage({ type: 'progress', hashed: r.hashed });
    }
    setTimeout(step, 0); // macrotask boundary -> lets new 'work' messages arrive
  }
  step();
}
