import test from 'node:test';
import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
import { navigate,errorDetails } from './navigation.mjs';
import { Sessions,sessionOwner } from './sessions.mjs';
import { validateInput,extractPage } from './web.mjs';

test('assistant scope isolates concurrent browsers and rejects cross-assistant session IDs',async()=>{
 const s=new Sessions({create:async()=>({browser:{close:async()=>{}}})});
 const owner=agent_id=>sessionOwner({user_id:'same-user',agent_id});
 const [a,b]=await Promise.all([s.acquire(owner('research')),s.acquire(owner('writing'))]);
 assert.notEqual(a.id,b.id);s.release(a);s.release(b);
 await assert.rejects(s.acquire(owner('research'),b.id),{status:404});
 assert.equal(await s.acquire(owner('research'),a.id),a);
 assert.equal(owner(undefined),owner('default'));
 assert.notEqual(sessionOwner({user_id:'a:b',agent_id:'c'}),sessionOwner({user_id:'a',agent_id:'b:c'}));
 for(const agent_id of ['',{},'../x','a'.repeat(101)])assert.equal(validateInput({user_id:'u',agent_id,action:'open',url:'https://example.com'}),false);
 await s.shutdown();
});
test('error classification distinguishes timeout, network failure and source denial',()=>{
 assert.equal(errorDetails({name:'TimeoutError'}).code,'navigation_timeout');
 assert.equal(errorDetails(Error('page.goto: net::ERR_TUNNEL_CONNECTION_FAILED at secret-url')).network_error,'ERR_TUNNEL_CONNECTION_FAILED');
 assert.ok(!JSON.stringify(errorDetails(Error('sensitive body'))).includes('sensitive'));
});
test('real browser preserves readable pages despite stalled scripts and retries empty parser-blocked pages', {skip:!process.env.BROWSER_TEST_EXECUTABLE}, async t=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_TEST_EXECUTABLE,chromiumSandbox:false});
 t.after(()=>browser.close());
 await t.test('publication outside article and collapsed content remain visible as metadata',async()=>{
  const page=await browser.newPage();
  await page.setContent('<span id="news-time">2025-09-14 21:01</span><article>Article text<button>展开全文</button></article>');
  const data=await page.evaluate(extractPage,1);
  assert.equal(data.published_at,'2025-09-14 21:01');assert.equal(data.content_collapsed,true);
  assert.ok(!data.text.includes('2025'));await page.close();
 });
 for(const kind of ['body-before-script' ,'body-after-script','denied','challenge','blank'])await t.test(kind,async()=>{
  const state={readMode:false};const context=await browser.newContext();
  try{
   await context.route('**/*',async route=>{
    const url=new URL(route.request().url());
    if(url.pathname==='/slow.js'){if(state.readMode)await route.abort();return;}
    const content='<main><h1>Verified article</h1><p>'+('Original article body. '.repeat(30))+'</p></main>';
    const script='<script src="/slow.js"></script>';
    const html=kind==='body-after-script'?'<head>'+script+'</head><body>'+content+'<script>document.body.textContent=\"inline replaced body\";</script></body>':kind==='body-before-script'?'<body>'+content+script+'</body>':kind==='denied'?'Access denied':kind==='challenge'?'<title>Just a moment...</title><body></body>':'<body></body>';
    await route.fulfill({status:kind==='denied'?403:200,contentType:'text/html',body:html});
   });
   const page=await context.newPage();const session={page,state,refs:new Set()};
   if(kind==='denied'){await assert.rejects(navigate(session,'https://fixture.example/',{domMs:150}),e=>e.details.upstream_status===403);assert.equal(state.readMode,false);}
   else if(kind==='challenge'){await assert.rejects(navigate(session,'https://fixture.example/',{domMs:150}),e=>e.details.code==='verification_required');assert.equal(state.readMode,false);}
   else if(kind==='blank')await assert.rejects(navigate(session,'https://fixture.example/',{domMs:150}),e=>e.details.code==='empty_document');
   else{
    await navigate(session,'https://fixture.example/',{domMs:150});
    assert.match(await page.locator('body').innerText(),/Verified article/);
    assert.equal(state.readMode,kind==='body-after-script');
    assert.equal(session.loadState,kind==='body-after-script'?'dom_ready':'partial');
   }
  }finally{await context.close();}
 });
});
