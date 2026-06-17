// Smoke-test the WASM hasher in Node: load it, call cereblixMine, and confirm
// the exported function works and respects the target.
const fs = require('fs');
require('/var/www/html/cereblix/wasm_exec.js');
const go = new Go();
(async () => {
  const buf = fs.readFileSync('/var/www/html/cereblix/cereblix.wasm');
  const res = await WebAssembly.instantiate(buf, go.importObject);
  go.run(res.instance); // registers globalThis.cereblixMine, then parks
  const header = Buffer.alloc(124).fill(7).toString('hex');
  const seed = Buffer.alloc(32).fill(9).toString('hex');
  const allFF = 'ff'.repeat(32); // trivially satisfiable target
  const all00 = '00'.repeat(32); // impossible target

  const easy = globalThis.cereblixMine(header, allFF, seed, 0, '0', 4);
  console.log('easy target ->', JSON.stringify(easy));
  const hard = globalThis.cereblixMine(header, all00, seed, 0, '0', 4);
  console.log('hard target ->', JSON.stringify(hard));

  if (easy.found && !hard.found && hard.hashed === 4) console.log('WASM_OK');
  else console.log('WASM_FAIL');
  process.exit(0);
})();
