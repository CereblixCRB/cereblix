const puppeteer = require('/tmp/node_modules/puppeteer');
(async () => {
  const b = await puppeteer.launch({ args: ['--no-sandbox'], headless: 'new' });
  // desktop
  let pg = await b.newPage();
  await pg.setViewport({ width: 1280, height: 900, deviceScaleFactor: 1 });
  await pg.goto('http://127.0.0.1/cereblix/', { waitUntil: 'networkidle2', timeout: 25000 }).catch(()=>{});
  await new Promise(r => setTimeout(r, 1800));
  await pg.screenshot({ path: '/tmp/d-hero.png' });
  await pg.screenshot({ path: '/tmp/d-full.png', fullPage: true });
  // mobile
  let m = await b.newPage();
  await m.setViewport({ width: 390, height: 850, isMobile: true, deviceScaleFactor: 2 });
  await m.goto('http://127.0.0.1/cereblix/', { waitUntil: 'networkidle2', timeout: 25000 }).catch(()=>{});
  await new Promise(r => setTimeout(r, 1800));
  await m.screenshot({ path: '/tmp/m-hero.png' });
  await b.close();
  console.log('shots ok');
})();
