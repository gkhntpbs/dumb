// Add worker / Re-pair acceptance against two admin previews (cmd/admin-preview):
//   QA_ADMIN_URL      preview with QA_ADMIN_PAIRING=on and QA_ADMIN_PUBLIC_URL=https://dumb.example.com
//   QA_ADMIN_URL_OFF  preview with QA_ADMIN_PAIRING=off
// Run with PLAYWRIGHT_MODULE, CHROME_BIN and QA_ADMIN_PASSWORD_FILE (or QA_ADMIN_PASSWORD).
// Screenshots go to docs/qa/add-worker-2026-10-15/. Neither the password nor a
// pairing code is printed; preview codes are throwaway values nothing accepts.
import {readFileSync} from 'node:fs';
import {pathToFileURL,fileURLToPath} from 'node:url';
import assert from 'node:assert/strict';
const {chromium}=await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href);
const base=process.env.QA_ADMIN_URL||'http://127.0.0.1:52147';
const baseOff=process.env.QA_ADMIN_URL_OFF;assert.ok(baseOff,'QA_ADMIN_URL_OFF is required');
const publicURL='https://dumb.example.com';
const password=process.env.QA_ADMIN_PASSWORD||readFileSync(process.env.QA_ADMIN_PASSWORD_FILE,'utf8').trim();
const out=fileURLToPath(new URL('../../docs/qa/add-worker-2026-10-15/',import.meta.url));
const browser=await chromium.launch({executablePath:process.env.CHROME_BIN,headless:true});
const errors=[],logs=[],pages=[];
async function open(url,viewport={width:1440,height:1000}){
  const context=await browser.newContext({viewport});await context.grantPermissions(['clipboard-read','clipboard-write'],{origin:url});
  const page=await context.newPage();pages.push(page);page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>logs.push(m.text()));
  const posts=[];page.on('request',r=>{if(r.method()==='POST'&&r.url().endsWith('/admin/v1/pairing'))posts.push({body:r.postData(),headers:r.allHeaders()});});
  await page.clock.install();
  await page.goto(url);await page.locator('#login-status').filter({hasText:'Sign in to continue'}).waitFor();
  await page.locator('#password').fill(password);await page.locator('#login-submit').click();
  await page.locator('#status').filter({hasText:'Live'}).waitFor();
  await page.locator('[data-view="workers"]').click();
  return {context,page,posts};
}
// Every visible descendant of the dialog stays inside it horizontally; nothing scrolls sideways.
const fits=page=>page.evaluate(()=>{const d=document.querySelector('dialog[open]');const r=d.getBoundingClientRect();const bad=[...d.querySelectorAll('*')].filter(n=>{if(!n.getClientRects().length||n.closest('pre'))return false;const b=n.getBoundingClientRect();return b.width>0&&(b.left<r.left-0.5||b.right>r.right+0.5);}).map(n=>n.id||n.tagName);return{bad,dialogOverflow:d.scrollWidth>d.clientWidth,pageOverflow:document.documentElement.scrollWidth>innerWidth,preOverflow:[...d.querySelectorAll('pre')].some(p=>p.scrollWidth>p.clientWidth+1)};});
const assertFits=async(page,label)=>{const f=await fits(page);assert.deepEqual(f,{bad:[],dialogOverflow:false,pageOverflow:false,preOverflow:false},label);};
// Commands use shell line continuations; flatten them to check the arguments.
const flat=c=>c.replace(/ \\\n\s+/g,' ').replace(/ `\n\s+/g,' ').replace(/\|\n\s+/g,'| ');
const rawCommands=page=>page.locator('#pairing-commands pre code').allTextContents();
const commandTexts=async page=>(await rawCommands(page)).map(flat);
const noCodeAnywhere=async(page,code,label)=>{assert.equal(await page.evaluate(c=>document.documentElement.outerHTML.includes(c),code),false,`${label}: code still in the DOM`);assert.equal(await page.evaluate(c=>[...Object.values(localStorage),...Object.values(sessionStorage)].some(v=>v.includes(c)),code),false,`${label}: code in storage`);};
try{
  // 1. Enabled preview with a configured public URL.
  const {context,page,posts}=await open(base);
  const add=page.locator('#add-worker');await add.waitFor();
  assert.equal(await add.isEnabled(),true);assert.equal(await page.locator('#add-worker-note').isVisible(),false);
  await page.screenshot({path:out+'devices.png',fullPage:true});
  await add.click();await page.locator('#pairing').waitFor();
  assert.equal(await page.locator('#pairing-title').textContent(),'Add worker');
  assert.equal(await page.locator('#pairing-server').isVisible(),false);
  assert.match(await page.locator('#pairing-server-source').textContent(),/https:\/\/dumb\.example\.com/);
  assert.equal(await page.locator('#pairing-ttl').inputValue(),'600');assert.equal(await page.locator('#pairing-credential-ttl').inputValue(),'31536000');
  await page.locator('#pairing-name').fill("Kitchen Pi 5");
  await assertFits(page,'empty dialog');
  await page.screenshot({path:out+'dialog-empty.png'});
  // A double click must not create two codes.
  await page.locator('#pairing-generate').dblclick();
  await page.locator('#pairing-code').filter({hasText:/^[A-Za-z0-9_-]{43}$/}).waitFor();
  await page.waitForTimeout(300);assert.equal(posts.length,1,'one POST for a double click');
  const sent=await posts[0].headers;assert.equal(sent.origin,new URL(base).origin);assert.match(sent['content-type'],/^application\/json/);
  assert.deepEqual(JSON.parse(posts[0].body),{ttl_seconds:600,credential_ttl_seconds:31536000});
  const code=await page.locator('#pairing-code').textContent();
  assert.match(await page.locator('#pairing-worker-id').textContent(),/^[A-Za-z0-9_-]{43}$/);
  const first=await page.locator('#pairing-countdown').textContent();assert.match(first,/^Expires in (10:00|9:5\d)/);
  await page.clock.fastForward(3000);const later=await page.locator('#pairing-countdown').textContent();assert.notEqual(later,first,'countdown moves');assert.match(later,/^Expires in 9:5\d/);
  const shots={linux:'generated-linux.png',macos:'generated-macos.png',windows:'generated-windows.png'};
  const want={linux:[/^curl -fsSLO https:\/\/raw\.githubusercontent\.com\/gkhntpbs\/dumb\/main\/scripts\/install\.sh$/,/^sudo sh install\.sh -system -server 'https:\/\/dumb\.example\.com' -models '[^']+' -name 'Kitchen Pi 5' -pair-code-stdin$/],macos:[/\.\/dumb-worker pair -server 'https:\/\/dumb\.example\.com' -token-file "\$HOME\/Library\/Application Support\/DUMB\/worker-token"$/,/^\.\/dumb-worker service install -server 'https:\/\/dumb\.example\.com' -token-file .* -name 'Kitchen Pi 5'$/],windows:[/Read-Host 'Pairing code' -AsSecureString[\s\S]*\.\\dumb-worker\.exe pair -server 'https:\/\/dumb\.example\.com' -token-file \$Token/,/\$env:DUMB_WORKER_NAME = 'Kitchen Pi 5'\n\.\\dumb-worker\.exe$/]};
  for(const os of ['linux','macos','windows']){
    await page.locator(`#pairing-tab-${os}`).click();assert.equal(await page.locator(`#pairing-tab-${os}`).getAttribute('aria-selected'),'true');
    const cmds=await commandTexts(page),raw=await rawCommands(page);assert.equal(cmds.length,2,os);want[os].forEach((re,i)=>assert.match(cmds[i],re,`${os} step ${i+1}`));
    for(const c of raw)assert.ok(!c.includes(code),`${os}: code on a command line`);
    await assertFits(page,`${os} tab`);
    await page.locator('#pairing').screenshot({path:out+shots[os]});
    const buttons=page.locator('#pairing-commands .command-head button');assert.equal(await buttons.count(),2);
    for(let i=0;i<2;i++){await buttons.nth(i).click();assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),raw[i],`${os} copy ${i+1}`);}
  }
  await page.locator('#pairing-copy-code').click();assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),code,'copy code');
  assert.equal(await page.locator('#pairing-copy-code').textContent(),'Copied');
  await page.locator('#pairing-tab-linux').focus();await page.keyboard.press('ArrowRight');assert.equal(await page.locator('#pairing-tab-macos').getAttribute('aria-selected'),'true','arrow keys move between tabs');
  // Expiry: the code disappears and a new one can be generated.
  await page.clock.fastForward(10*60*1000);
  await page.locator('#pairing-countdown').filter({hasText:'Expired'}).waitFor();await page.locator('#pairing-tab-linux').click();await page.evaluate(()=>document.activeElement?.blur());
  assert.match(await page.locator('#pairing-code-label').textContent(),/expired and removed/);
  assert.equal(await page.locator('#pairing-code').textContent(),'');assert.equal(await page.locator('#pairing-copy-code').isDisabled(),true);assert.equal(await page.locator('#pairing-again').isVisible(),true);
  await noCodeAnywhere(page,code,'expired');
  await page.locator('#pairing').screenshot({path:out+'expired.png'});
  // Close erases the code from the DOM (Escape path).
  await page.locator('#pairing-again').click();await page.locator('#pairing-generate').click();
  await page.locator('#pairing-code').filter({hasText:/^[A-Za-z0-9_-]{43}$/}).waitFor();const code2=await page.locator('#pairing-code').textContent();assert.notEqual(code2,code);
  await page.keyboard.press('Escape');await page.waitForFunction(()=>!document.getElementById('pairing').open);
  await noCodeAnywhere(page,code2,'after Escape');
  await page.locator('#add-worker').click();await page.locator('#pairing-generate').click();await page.locator('#pairing-code').filter({hasText:/^[A-Za-z0-9_-]{43}$/}).waitFor();const code3=await page.locator('#pairing-code').textContent();
  await page.locator('#pairing-close').click();await noCodeAnywhere(page,code3,'after Close');
  // Re-pair from the worker details keeps the identity.
  const row=page.locator('#workers td:first-child button',{hasText:'Living room Pi 5'});await row.click();
  const workerId=(await page.locator('#details-content dd').nth(1).textContent());
  assert.equal(await page.locator('#details-repair').isEnabled(),true);await page.locator('#details-repair').click();
  await page.locator('#pairing-title').filter({hasText:'Re-pair Living room Pi 5'}).waitFor();
  assert.equal(await page.locator('#details').evaluate(d=>d.open),false);
  await page.locator('#pairing-generate').click();await page.locator('#pairing-code').filter({hasText:/^[A-Za-z0-9_-]{43}$/}).waitFor();
  assert.equal(JSON.parse(posts.at(-1).body).worker_id,workerId);assert.equal(await page.locator('#pairing-worker-id').textContent(),workerId);
  const repair=await commandTexts(page);assert.match(repair[0],/install -system -server 'https:\/\/dumb\.example\.com' -models '[^']+' -name 'Living room Pi 5' -pair-code-stdin -force-pair$/);
  await assertFits(page,'re-pair');await page.locator('#pairing').screenshot({path:out+'repair.png'});
  // 390 px mobile.
  await page.setViewportSize({width:390,height:844});await page.waitForTimeout(200);
  await assertFits(page,'mobile re-pair');await page.screenshot({path:out+'mobile-dialog.png'});
  await page.locator('#pairing-tab-windows').click();await assertFits(page,'mobile windows');await page.locator('#pairing').evaluate(d=>d.scrollTop=d.scrollHeight);await page.screenshot({path:out+'mobile-dialog-windows.png'});
  const mcode=await page.locator('#pairing-code').textContent();await page.locator('#pairing-close').click();await noCodeAnywhere(page,mcode,'mobile close');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  await page.evaluate(()=>scrollTo(0,0));const box=await page.locator('#add-worker').boundingBox();assert.ok(box&&box.x>=0&&box.x+box.width<=390,'button inside the viewport');
  await page.locator('.pairing-entry').locator('xpath=ancestor::section[1]').screenshot({path:out+'mobile-devices.png'});
  // Without a configured public URL: editable, validated, only the URL remembered.
  await page.setViewportSize({width:1440,height:1000});
  await page.route('**/admin/v1/session',async route=>{const r=await route.fetch();const j=await r.json();delete j.public_url;await route.fulfill({response:r,json:j});});
  await page.reload();await page.locator('#status').filter({hasText:'Live'}).waitFor();await page.locator('[data-view="workers"]').click();
  await page.locator('#add-worker').click();assert.equal(await page.locator('#pairing-server').isVisible(),true);
  await page.locator('#pairing-generate').click();await page.locator('#pairing-code').filter({hasText:/^[A-Za-z0-9_-]{43}$/}).waitFor();const code4=await page.locator('#pairing-code').textContent();
  assert.equal(await page.locator('#pairing-commands pre').count(),0);
  await page.locator('#pairing-server').fill('http://dumb.example.com');await page.locator('#pairing-server').blur();
  assert.equal(await page.locator('#pairing-commands pre').count(),0);assert.match(await page.locator('#pairing-field-error').textContent(),/https URL/);
  await page.locator('#pairing-server').fill('https://workers.example.org/');await page.locator('#pairing-server').blur();
  assert.equal(await page.locator('#pairing-server').inputValue(),'https://workers.example.org');
  assert.match((await commandTexts(page))[1],/-server 'https:\/\/workers\.example\.org'/);
  assert.deepEqual(await page.evaluate(()=>({...localStorage})),{'dumb.admin.workerServerUrl':'https://workers.example.org'});
  assert.equal(await page.evaluate(()=>sessionStorage.length),0);
  await assertFits(page,'server URL fallback');await page.locator('#pairing').screenshot({path:out+'server-url-fallback.png'});
  await page.locator('#pairing-close').click();await noCodeAnywhere(page,code4,'fallback close');
  for(const c of [code,code2,code3,mcode,code4])assert.ok(!logs.some(l=>l.includes(c)),'code in console output');
  await context.close();

  // 2. Preview without credential management.
  const off=await open(baseOff);
  await off.page.locator('#add-worker-note').filter({hasText:'turned off'}).waitFor();
  assert.equal(await off.page.locator('#add-worker').isEnabled(),true);
  await off.page.locator('.pairing-entry').locator('xpath=ancestor::section[1]').screenshot({path:out+'disabled-devices.png'});
  await off.page.locator('#add-worker').click();await off.page.locator('#pairing-unavailable').waitFor();
  assert.equal(await off.page.locator('#pairing-form').isVisible(),false);assert.equal(off.posts.length,0);
  await assertFits(off.page,'disabled');await off.page.locator('#pairing').screenshot({path:out+'disabled-dialog.png'});
  await off.page.locator('#pairing-close').click();
  await off.page.locator('#workers td:first-child button').first().click();
  assert.equal(await off.page.locator('#details-repair').isEnabled(),true);assert.match(await off.page.locator('#details-repair-note').textContent(),/turned off/);
  await off.context.close();
  assert.deepEqual(errors,[]);
  console.log('Add worker acceptance passed: button, empty dialog, single code on double click, countdown, copy, Linux/macOS/Windows commands without the code, expiry, Escape/Close erase the code, re-pair keeps the worker id, 390 px, server URL fallback, disabled server. No code or password printed.');
}catch(error){if(process.env.QA_FAILURE_SHOT)for(const p of pages)if(!p.isClosed())await p.screenshot({path:process.env.QA_FAILURE_SHOT}).catch(()=>{});throw error;}finally{await browser.close();}
