// Worker display-name acceptance against the admin preview (cmd/admin-preview).
// Run with PLAYWRIGHT_MODULE, CHROME_BIN, QA_ADMIN_PASSWORD_FILE and optional QA_ADMIN_URL.
// Screenshots go to docs/qa/worker-display-name-2026-10-15/. Credentials are never printed.
import {readFileSync} from 'node:fs';
import {pathToFileURL,fileURLToPath} from 'node:url';
import assert from 'node:assert/strict';
const {chromium}=await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href);
const browser=await chromium.launch({executablePath:process.env.CHROME_BIN,headless:true});
const context=await browser.newContext({viewport:{width:1440,height:1100}});
const page=await context.newPage();
const errors=[];page.on('pageerror',e=>errors.push(e.message));
const base=process.env.QA_ADMIN_URL||'http://127.0.0.1:52147';
const password=process.env.QA_ADMIN_PASSWORD||readFileSync(process.env.QA_ADMIN_PASSWORD_FILE,'utf8').trim();
const out=fileURLToPath(new URL('../../docs/qa/worker-display-name-2026-10-15/',import.meta.url));
const named=['Studio Mac mini','Living room Pi 5','Research lab workstation with an exceptionally long display name'];
// Every card and its children stay inside the card; titles wrap at most to two lines.
const layout=()=>page.evaluate(()=>[...document.querySelectorAll('#device-cards .device-card')].map(card=>{const r=card.getBoundingClientRect(),h3=card.querySelector('h3'),id=card.querySelector('.device-id');const inside=[...card.children].every(c=>{const b=c.getBoundingClientRect();return b.left>=r.left-0.5&&b.right<=r.right+0.5;});const line=parseFloat(getComputedStyle(h3).lineHeight)||h3.getBoundingClientRect().height;return{title:h3.textContent,full:h3.title,id:id.textContent,idTitle:id.title,inside,cardOverflow:card.scrollWidth>card.clientWidth,titleLines:Math.round(h3.getBoundingClientRect().height/line),idEllipsis:getComputedStyle(id).textOverflow,idOneLine:id.getBoundingClientRect().height<=parseFloat(getComputedStyle(id).lineHeight)+1};}));
try{
await page.goto(base);
await page.locator('#login-status').filter({hasText:'Sign in to continue'}).waitFor();
await page.locator('#password').fill(password);await page.locator('#login-submit').click();
await page.locator('#status').filter({hasText:'Live'}).waitFor();
await page.locator('[data-view="workers"]').click();
await page.locator('#device-cards .device-card').nth(3).waitFor();
const cards=await layout();
assert.equal(cards.length,4);
for(const name of named)assert.ok(cards.some(c=>c.title===name&&/^[A-Za-z0-9_-]{43}$/.test(c.id)&&c.idTitle===c.id),`card for ${name}`);
const nameless=cards.find(c=>c.id==='No name reported');
assert.ok(nameless&&/^[A-Za-z0-9_-]{43}$/.test(nameless.title)&&nameless.titleLines===1,'nameless card falls back to the id on one line');
for(const c of cards){assert.ok(c.inside&&!c.cardOverflow,`overflow in ${c.title}`);assert.ok(c.titleLines<=2,`${c.title} uses ${c.titleLines} lines`);assert.equal(c.idEllipsis,'ellipsis');assert.ok(c.idOneLine);}
await page.locator('#device-cards').scrollIntoViewIfNeeded();
await page.locator('#device-cards').screenshot({path:out+'device-cards.png'});
await page.screenshot({path:out+'devices.png',fullPage:true});
const rows=await page.locator('#workers tr').evaluateAll(trs=>trs.map(tr=>[...tr.children].slice(0,2).map(td=>td.textContent)));
assert.equal(rows.length,4);
for(const name of named)assert.ok(rows.some(([n,id])=>n===name&&id.length===43),`row for ${name}`);
assert.ok(rows.some(([n,id])=>n===id),'nameless row falls back to the id');
assert.equal(await page.locator('table:has(#workers) th').first().textContent(),'Name');
// A name that fits is not clipped by a pixel; one that does not fit ends in an ellipsis.
for(const fit of await page.locator('#workers td:first-child button').evaluateAll(bs=>bs.map(b=>{const r=document.createRange();r.selectNodeContents(b);return{text:b.textContent,text_w:r.getBoundingClientRect().width,box:b.clientWidth,ellipsis:b.scrollWidth>b.clientWidth&&getComputedStyle(b).textOverflow==='ellipsis'};})))assert.ok(fit.ellipsis||fit.text_w<=fit.box,`clipped name ${fit.text}`);
assert.ok(await page.locator('#workers').locator('xpath=ancestor::div[1]').evaluate(w=>w.scrollWidth<=w.clientWidth),'workers table fits at 1440px');
const table=page.locator('#workers').locator('xpath=ancestor::section[1]');
await table.scrollIntoViewIfNeeded();await table.screenshot({path:out+'workers-table.png'});
await page.locator('#workers-search').fill('living room');
assert.deepEqual(await page.locator('#workers tr td:first-child').allTextContents(),['Living room Pi 5']);
await page.locator('#workers-search').fill('');
const details=async(name,file)=>{await page.locator('#workers td:first-child button',{hasText:name}).click();await page.locator('#details').waitFor();const dl=await page.locator('#details-content > *').allTextContents();await page.screenshot({path:out+file});await page.locator('#details-close').click();return dl;};
const longDetails=await details(named[2],'details-long-name.png');
assert.deepEqual(longDetails.slice(0,2),['Name',named[2]]);assert.equal(longDetails[2],'Worker ID');assert.equal(longDetails[3].length,43);
const namelessDetails=await details(nameless.title,'details-nameless.png');
assert.deepEqual(namelessDetails.slice(0,4),['Name','Not reported','Worker ID',nameless.title]);
await page.setViewportSize({width:390,height:844});
await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth);
for(const c of await layout())assert.ok(c.inside&&!c.cardOverflow&&c.titleLines<=2,`mobile overflow in ${c.title}`);
await page.locator('#device-cards').screenshot({path:out+'device-cards-mobile.png'});
assert.deepEqual(errors,[]);
console.log('Worker name acceptance passed: cards (named, nameless, 64-character), table, search, details, mobile; no overflow.');
}finally{await browser.close();}
