const puppeteer = require('/tmp/node_modules/puppeteer');
const pages = [
  ['http://127.0.0.1/cereblix/mine.html', '/tmp/p-mine.png', 560, 1000],
  ['http://127.0.0.1/cereblix/explorer.html', '/tmp/p-explorer.png', 1100, 950],
  ['http://127.0.0.1/cereblix/wallet/', '/tmp/p-wallet.png', 900, 900],
];
(async () => {
  const b = await puppeteer.launch({ args: ['--no-sandbox'], headless: 'new' });
  for (const [url, out, w, h] of pages) {
    const pg = await b.newPage();
    await pg.setViewport({ width: w, height: h, deviceScaleFactor: 1 });
    await pg.goto(url, { waitUntil: 'networkidle2', timeout: 25000 }).catch(()=>{});
    await new Promise(r => setTimeout(r, 1600));
    await pg.screenshot({ path: out });
    await pg.close();
  }
  await b.close();
  console.log('shots ok');
})();
