import { BrowserError } from './sessions.mjs';
import { isChallengePage } from './web.mjs';

function checkResponse(response) {
  if (!response) throw new BrowserError(502,'No document response; try another source',{code:'no_document',retryable:true});
  const status=response.status();
  if(status>=400)throw new BrowserError(502,`Source returned HTTP ${status}; choose another source`,{code:'upstream_http_error',upstream_status:status,retryable:status>=500});
  if(!/text\/html|application\/xhtml\+xml|text\/plain/.test(response.headers()['content-type']||''))throw new BrowserError(422,'Only HTML and plain text pages are supported',{code:'unsupported_content',retryable:false});
}
async function settle(page,timeout){
  try{await page.waitForLoadState('domcontentloaded',{timeout});return true;}
  catch(e){if(e.name!=='TimeoutError')throw e;return false;}
}
const bodyText=page=>page.evaluate(()=>document.body?.innerText?.trim()||'');
// A slow script must not turn an already readable HTTP 200 article into a 502.
// Retry an empty, parser-blocked document once without scripts. Never retry a
// source's HTTP denial or verification page in an attempt to evade its policy.
export async function setReadingMode(session,enabled) {
  session.state.readMode=enabled;
  if(enabled&&!session.scriptControl)session.scriptControl=await session.page.context().newCDPSession(session.page);
  if(session.scriptControl)await session.scriptControl.send('Emulation.setScriptExecutionDisabled',{value:enabled});
}
export async function navigate(session,target,{commitMs=10000,domMs=3500}={}) {
  const page=session.page;
  await setReadingMode(session,false);session.loadState=undefined;session.refs=new Set();
  let response=await page.goto(target,{waitUntil:'commit',timeout:commitMs});
  checkResponse(response);
  let loaded=await settle(page,domMs);
  if(isChallengePage({title:await page.title(),text:await bodyText(page)}))throw new BrowserError(422,'Site requires verification; choose another source',{code:'verification_required',retryable:false});
  if(!loaded&&(await bodyText(page)).length<160){
    await setReadingMode(session,true);
    response=await page.goto(page.url(),{waitUntil:'commit',timeout:commitMs});
    checkResponse(response);loaded=await settle(page,domMs);
  }
  if(!(await bodyText(page)).trim())throw new BrowserError(422,'Document has no readable body; choose another source',{code:'empty_document',retryable:false});
  session.loadState=loaded?'dom_ready':'partial';
  return response;
}
export function errorDetails(error) {
  if(error instanceof BrowserError)return error.details||{};
  if(error?.name==='TimeoutError')return {code:'navigation_timeout',retryable:true};
  const network=String(error?.message||'').match(/net::(ERR_[A-Z_]+)/)?.[1];
  return {code:network?'network_error':'browser_error',network_error:network,retryable:true};
}
