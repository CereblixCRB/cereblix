// Screenshot the site at a phone viewport to verify mobile layout (overflow).
const puppeteer = require('puppeteer');
const pages = [
  ['index', 'http://127.0.0.1/cereblix/'],
  ['explorer', 'http://127.0.0.1/cereblix/explorer.html'],
  ['mine', 'http://127.0.0.1/cereblix/mine.html'],
  ['wallet', 'http://127.0.0.1/cereblix/wallet/'],
];
(async () => {
  const browser = await puppeteer.launch({ args: ['--no-sandbox'], headless: 'new' });
  for (const [name, url] of pages) {
    const page = await browser.newPage();
    await page.setViewport({ width: 375, height: 820, isMobile: true, deviceScaleFactor: 1 });
    await page.goto(url, { waitUntil: 'networkidle2', timeout: 20000 }).catch(()=>{});
    await new Promise(r => setTimeout(r, 1500));
    // report any element wider than the viewport (horizontal overflow)
    const over = await page.evaluate(() => {
      const vw = document.documentElement.clientWidth;
      const bad = [];
      document.querySelectorAll('*').forEach(el => {
        const r = el.getBoundingClientRect();
        if (r.right > vw + 1 || r.left < -1) {
          bad.push((el.tagName.toLowerCase()) + (el.className && typeof el.className==='string' ? '.'+el.className.split(' ').join('.') : '') + ' right=' + Math.round(r.right) + ' vw=' + vw);
        }
      });
      return { vw, scrollW: document.documentElement.scrollWidth, bad: bad.slice(0, 12) };
    });
    console.log(`\n=== ${name} (vw=${over.vw}, scrollWidth=${over.scrollW}) ===`);
    console.log(over.scrollW > over.vw + 1 ? 'HORIZONTAL OVERFLOW' : 'no horizontal overflow');
    over.bad.forEach(b => console.log('  overflow:', b));
    await page.screenshot({ path: '/tmp/shot-' + name + '.png', fullPage: true });
    await page.close();
  }
  await browser.close();
  console.log('\nshots saved');
})();
