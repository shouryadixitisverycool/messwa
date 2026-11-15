 'use strict';
// Browser alerts run while a dashboard tab is open. No background delivery service.
function attentionUnseen(items, seen) { return items.filter(x => !seen.has(x.id)); }
if (typeof module !== 'undefined') module.exports = { attentionUnseen };
if (typeof document !== 'undefined') document.addEventListener('DOMContentLoaded', async () => {
 const user = await me(); if (!user) return;
 const key = 'messwa-attention-' + user.id + '-' + user.name;
 let memorySeen = [];
 const read = suffix => { try { return JSON.parse(localStorage.getItem(key+suffix) || JSON.stringify(memorySeen)); } catch { return memorySeen; } };
 const write = (suffix,value) => { memorySeen = value; try { localStorage.setItem(key+suffix,JSON.stringify(value)); } catch {} };
 const box = el('details','chest attention'); box.open = true;
 const summary = el('summary','chest-head'); const count = el('span','head-meta','Loading');
 count.setAttribute('role','status');
 summary.append(el('span','head-name','Attention needed'),count);
 const body = el('div','attention-body');const rows = el('div');const note = el('p','attention-note'); note.setAttribute('role','status');
 const enable = el('button','','Enable browser alerts');enable.type='button';
 enable.hidden = typeof Notification === 'undefined';
 const permissionText = () => { if (typeof Notification === 'undefined') return; enable.textContent=Notification.permission==='granted' ? 'Browser alerts enabled' : Notification.permission==='denied' ? 'Browser alerts blocked' : 'Enable browser alerts';enable.disabled=Notification.permission!=='default'; };
 enable.onclick=async()=>{try{await Notification.requestPermission();permissionText();}catch{note.textContent='Browser alerts are unavailable here.';}};permissionText();
 body.append(rows,enable,note);box.append(summary,body);document.querySelector('.top').after(box);
 let busy=false;
 async function openSale(id){const r=await fetch('api/attention-sale?id='+id,{cache:'no-store'});if(!r.ok)throw Error('Could not open sale');const sale=await r.json();openMealSheet(sale);}
 async function refresh(){if(busy)return;busy=true;try{
 const r=await fetch('api/attention',{cache:'no-store'});if(r.status===401){box.hidden=true;return;}if(r.status===403){box.hidden=true;return;}if(!r.ok)throw Error('Attention items could not be refreshed.');
 const items=await r.json();box.hidden=false;count.textContent=String(items.length);rows.replaceChildren();note.textContent='Browser alerts work while this dashboard is open. Mark resolved only after handling the issue.';
 if(!items.length)rows.append(el('p','attention-note','Nothing needs your attention.'));
 for(const item of items){const row=el('div','attention-row'),copy=el('div','attention-copy'),actions=el('div','attention-actions');copy.append(el('strong','',item.title),el('p','',[item.date,item.meal,item.detail].filter(Boolean).join(' - ')));
 const link=el('a','',item.account?'Reconnect WhatsApp':item.task?'Open sale':'View meal');link.href=item.account?'account.html':item.task?'./#attention-sale='+item.task:'sales.html#attention-meal='+encodeURIComponent(item.date+'|'+item.meal);
 if(item.task && typeof openMealSheet==='function')link.onclick=async e=>{e.preventDefault();try{await openSale(item.task);}catch(err){note.textContent=err.message;}};
 actions.append(link);
 if(item.resolvable){const done=el('button','','Mark resolved');done.type='button';done.onclick=async()=>{done.disabled=true;try{await send('POST','api/attention',{id:item.id});await refreshAfter();}catch(e){note.textContent=e.message;done.disabled=false;}};actions.append(done);}
 row.append(copy,actions);rows.append(row);}
 const current=new Set(items.map(x=>x.id));
 const seen=new Set(read('-seen').filter(id=>current.has(id)));const unseen=attentionUnseen(items,seen);
 if(typeof Notification!=='undefined'&&Notification.permission==='granted')for(const item of unseen){try{const n=new Notification(item.title,{body:'Open Messwa to review this issue.',tag:item.id});n.onclick=()=>{window.focus();location.href=item.account?'account.html':item.task?'./#attention-sale='+item.task:'sales.html#attention-meal='+encodeURIComponent(item.date+'|'+item.meal);n.close();};seen.add(item.id);}catch{}}
 write('-seen',[...seen].slice(-2000));
 const match=/^#attention-sale=(\d+)$/.exec(location.hash);if(match&&typeof openMealSheet==='function'&&typeof D!=='undefined'&&D){history.replaceState(null,'',location.pathname+location.search);await openSale(Number(match[1]));}
 }catch(e){note.textContent=e.message+' Showing the last loaded items.';}finally{busy=false;}}
 async function refreshAfter(){busy=false;await refresh();}
 await refresh();setInterval(()=>{if(document.visibilityState==='visible')refresh();},15000);
 window.addEventListener('hashchange',refresh);
 document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible')refresh();});
});
