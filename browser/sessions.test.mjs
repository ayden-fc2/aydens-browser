import test from 'node:test';
import assert from 'node:assert/strict';
import { Sessions } from './sessions.mjs';
import { validURL, validateInput } from './web.mjs';
const create=async()=>({browser:{close:async()=>{}},page:{}});
test('separate users get distinct browser instances; one user reuses its instance',async()=>{
 const s=new Sessions({create});const [a,b]=await Promise.all([s.acquire('daily:1'),s.acquire('daily:2')]);assert.notEqual(a.id,b.id);assert.notEqual(a.browser,b.browser);
 await assert.rejects(s.acquire('daily:1'),{status:409});s.release(a);const reused=await s.acquire('daily:1');assert.equal(reused,a);
 s.release(a);await assert.rejects(s.acquire('daily:1',b.id),{status:404});await s.shutdown();assert.equal(s.users.size,0);
});
test('concurrent capacity reservation cannot launch extra browsers',async()=>{
 let launched=0;const s=new Sessions({max:2,create:async()=>{launched++;await new Promise(r=>setTimeout(r,10));return create();}});
 const out=await Promise.allSettled(['a','b','c','d'].map(u=>s.acquire(u)));assert.equal(out.filter(r=>r.status==='fulfilled').length,2);assert.equal(launched,2);await s.shutdown();
});
test('idle expiration closes process, rejects stale ID, creates clean instance',async()=>{
 let now=0,closed=0;const s=new Sessions({create:async()=>({browser:{close:async()=>closed++}}),now:()=>now,idleMs:100,ttlMs:500});
 const a=await s.acquire('a');s.release(a);now=101;await s.sweep();assert.equal(closed,1);assert.equal(s.users.size,0);await assert.rejects(s.acquire('a',a.id),{status:404});const next=await s.acquire('a');assert.notEqual(next.id,a.id);await s.shutdown();
});
test('absolute TTL expires active reuse; busy operation is not reclaimed midflight',async()=>{
 let now=0;const s=new Sessions({create,now:()=>now,idleMs:100,ttlMs:250});const a=await s.acquire('a');now=200;await s.sweep();assert.equal(s.users.size,1);s.release(a);now=251;await s.sweep();assert.equal(s.users.size,0);
});
test('launch failure releases reserved capacity',async()=>{
 const s=new Sessions({max:1,create:async()=>{throw Error('launch failed');}});await assert.rejects(s.acquire('a'));assert.equal(s.users.size,0);
});
test('strict operations do not expose arbitrary JS, credentials, selectors or files',()=>{
 for(const url of ['file:///etc/passwd','javascript:alert(1)','https://u:p@example.com','http://example.com:22'])assert.equal(validURL(url),false);
 assert.equal(validateInput({user_id:'daily:1',action:'open',url:'https://example.com'}),true);
 for(const extra of [{action:'evaluate',script:'process.env'},{action:'click',ref:'body'},{action:'open',url:'file:///etc/passwd'},{action:'fill',ref:'r1-0',text:'x',user_id:'../../1'},{action:'press',ref:'r1-0',key:'Control+L'}])assert.equal(validateInput({user_id:'daily:1',...extra}),false);
});
