const fs = require('fs');
const html = fs.readFileSync('/tmp/wallet-new.html', 'utf8');
const m = html.match(/function sha256[\s\S]*?return out;\n}/);
if (!m) { console.error('sha256 fn not found'); process.exit(1); }
eval(m[0]);
const hex = b => Array.from(b).map(x => x.toString(16).padStart(2, '0')).join('');
console.log('empty:', hex(sha256(new Uint8Array(0))));
console.log('abc  :', hex(sha256(new TextEncoder().encode('abc'))));
console.log('1000a:', hex(sha256(new Uint8Array(1000).fill(0x61))));
const pub = new Uint8Array(Buffer.from('592e4a009112f4889abb221101e658fa8a6f992066935caf6d514427b766eb8e', 'hex'));
console.log('addr :', 'crb1' + hex(sha256(pub).slice(0, 20)));
// expected:
// empty: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
// abc  : ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
// 1000a: 41edece42d63e8d9bf515a9ba6932e1c20cbc9f5a5d134645adb5db1b9737ea3
// addr : crb1abed37981bcd1786676c0490531e06c760d71712
