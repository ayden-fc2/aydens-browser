export function validURL(raw) {
  if (typeof raw !== 'string' || raw.length > 4096) return false;
  try { const u = new URL(raw); return ['http:','https:'].includes(u.protocol) && !u.username && !u.password && (!u.port || ['80','443'].includes(u.port)); } catch { return false; }
}
export function validateInput(input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return false;
  if (Object.keys(input).some(k=>!['user_id','session_id','action','url','query','engine','ref','text','key','direction','offset'].includes(k))) return false;
  if (typeof input.user_id !== 'string' || !/^[\w:@.-]{1,100}$/.test(input.user_id)) return false;
  if (input.session_id !== undefined && (typeof input.session_id !== 'string' || input.session_id.length>80)) return false;
  if (!['open','search','snapshot','click','fill','press','scroll','back','screenshot','close'].includes(input.action)) return false;
  if (input.action==='open'&&!validURL(input.url)) return false;
  if (input.action==='search'&&(typeof input.query!=='string'||!input.query.trim()||input.query.length>300||!['aggregate','duckduckgo','baidu',undefined].includes(input.engine))) return false;
  if (['click','fill','press'].includes(input.action)&&(typeof input.ref!=='string'||!/^r\d+-\d+$/.test(input.ref))) return false;
  if (input.action==='fill'&&(typeof input.text!=='string'||input.text.length>1000)) return false;
  if (input.action==='press'&&input.key!=='Enter') return false;
  if (input.action==='scroll'&&!['up','down'].includes(input.direction)) return false;
  if (input.offset!==undefined&&(!Number.isInteger(input.offset)||input.offset<0||input.offset>48000)) return false;
  return true;
}
// Runs inside the page; has no access to Node, secrets, or host files.
export function extractPage(generation) {
  const title=document.title.trim().slice(0,240);
  const published_at=document.querySelector('meta[property="article:published_time"],meta[name="date"],meta[name="pubdate"]')?.content||document.querySelector('time[datetime]')?.getAttribute('datetime')||'';
  const root=document.querySelector('article')||document.querySelector('main')||document.body;
  // innerText omits scripts and hidden content without mutating the live page.
  const text=(root?.innerText||'').replace(/\n{3,}/g,'\n\n').trim();
  const seen=new Set();
  const links=[...(root?.querySelectorAll('a[href]')||[])].flatMap(a=>{
    const url=a.href,label=a.innerText.trim().slice(0,160);
    if(!/^https?:\/\//.test(url)||!label||seen.has(url))return[];seen.add(url);return[{title:label,url}];
  }).slice(0,30);
  const elements=[];
  for(const el of document.querySelectorAll('a[href],button,input,textarea,select,[role="button"]')){
    if(elements.length>=60)break;
    const rect=el.getBoundingClientRect();if(!rect.width||!rect.height||el.disabled||['hidden','password','file'].includes(el.type))continue;
    const ref=`r${generation}-${elements.length}`;el.setAttribute('data-aydens-ref',ref);
    elements.push({ref,tag:el.tagName.toLowerCase(),type:el.type||undefined,value:['INPUT','TEXTAREA','SELECT'].includes(el.tagName)?el.value.slice(0,1000):undefined,label:(el.getAttribute('aria-label')||el.innerText||el.placeholder||el.name||'').trim().slice(0,120),url:el.tagName==='A'?el.href:undefined});
  }
  return{title,text:text.slice(0,60000),truncated:text.length>60000,links,elements,published_at};
}
