package remote

const terminalHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover,interactive-widget=resizes-content">
<meta name="color-scheme" content="dark">
<meta name="theme-color" content="#21252b">
<meta name="referrer" content="no-referrer">
<title>Midas remote</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.css">
<link rel="stylesheet" href="/client.css">
</head>
<body>
<main id="shell">
  <div id="terminal"></div>
  <nav id="keys" aria-label="Terminal keys">
    <button data-key="\u001b">Esc</button><button data-key="\u0003">Ctrl C</button>
    <button data-key="\t">Tab</button><button data-key="\u001b[D">←</button>
    <button data-key="\u001b[A">↑</button><button data-key="\u001b[B">↓</button>
    <button data-key="\u001b[C">→</button><button data-key="\r">Enter</button>
  </nav>
</main>
<div id="overlay" class="overlay"><div class="spinner" aria-label="Connecting"></div></div>
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/lib/addon-fit.js"></script>
<script src="/client.js"></script>
</body>
</html>`

const loginHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<meta name="referrer" content="no-referrer">
<title>Midas remote</title>
<link rel="stylesheet" href="/client.css">
</head>
<body class="login-body">
<form id="login" class="login">
  <div class="wordmark">Midas</div>
  <input id="password" type="password" placeholder="Remote password" autocomplete="current-password" autofocus>
  <button type="submit">Connect</button>
  <div id="error" class="error" role="alert"></div>
</form>
<script src="/login.js"></script>
</body>
</html>`

const clientCSS = `:root{color-scheme:dark}*{box-sizing:border-box}html,body{margin:0;width:100%;height:100%;overflow:hidden;background:#21252b}#shell{position:fixed;inset:0;display:flex;flex-direction:column}#terminal{flex:1 1 auto;min-height:0;padding:4px 4px 3px}.xterm{height:100%}#keys{display:none;flex:0 0 auto;gap:5px;padding:6px 5px calc(6px + env(safe-area-inset-bottom));overflow-x:auto;background:#1b1f25;border-top:1px solid #3e4452}#keys button{flex:0 0 auto;min-width:52px;height:40px;border:0;border-radius:7px;background:#2c313c;color:#abb2bf;font:600 13px ui-monospace,SFMono-Regular,Menlo,monospace;touch-action:manipulation}.overlay{position:fixed;inset:0;z-index:10;display:flex;align-items:center;justify-content:center;background:rgba(33,37,43,.82)}.overlay.hidden{display:none}.spinner{width:38px;height:38px;border:3px solid #3e4452;border-top-color:#61afef;border-radius:50%;animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}.login-body{display:flex;align-items:center;justify-content:center;font-family:ui-monospace,SFMono-Regular,Menlo,monospace}.login{width:min(340px,90vw)}.wordmark{margin-bottom:18px;color:#abb2bf;font-size:18px;font-weight:700}.login input{width:100%;padding:12px;background:#2c313c;color:#abb2bf;border:1px solid #3e4452;border-radius:8px;font:16px ui-monospace,SFMono-Regular,Menlo,monospace;outline:none}.login input:focus{border-color:#c678dd}.login button{width:100%;margin-top:12px;padding:11px 16px;border:0;border-radius:8px;background:#c678dd;color:#21252b;font:700 14px ui-monospace,SFMono-Regular,Menlo,monospace;cursor:pointer}.error{min-height:16px;margin-top:10px;color:#e06c75;font-size:12px}@media (pointer:coarse){#keys{display:flex}#terminal{padding-bottom:10px}}`

const loginJS = `(function(){"use strict";var form=document.getElementById("login"),password=document.getElementById("password"),error=document.getElementById("error");form.addEventListener("submit",function(event){event.preventDefault();error.textContent="";fetch("/api/login",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({password:password.value})}).then(function(response){if(response.ok){location.reload();return}return response.text().then(function(text){error.textContent=text||"Login failed"})}).catch(function(){error.textContent="Network error"})})})();`

const clientJS = `(function(){"use strict";
var theme={background:"#21252b",foreground:"#abb2bf",cursor:"#c678dd",cursorAccent:"#21252b",selectionBackground:"#3e4451",black:"#21252b",red:"#e06c75",green:"#98c379",yellow:"#e5c07b",blue:"#61afef",magenta:"#c678dd",cyan:"#56b6c2",white:"#abb2bf",brightBlack:"#5c6370",brightRed:"#e06c75",brightGreen:"#98c379",brightYellow:"#e5c07b",brightBlue:"#61afef",brightMagenta:"#c678dd",brightCyan:"#56b6c2",brightWhite:"#e5e5e5"};
var term=new Terminal({cursorBlink:true,cursorStyle:"bar",fontFamily:'"SF Mono",Menlo,Monaco,"DejaVu Sans Mono",monospace',fontSize:14,lineHeight:1,letterSpacing:0,scrollback:10000,macOptionIsMeta:true,theme:theme});
var fit=new FitAddon.FitAddon(),overlay=document.getElementById("overlay"),shell=document.getElementById("shell"),socket=null,retry=0,timer=null,viewport=window.visualViewport;
term.loadAddon(fit);term.open(document.getElementById("terminal"));
if(window.matchMedia&&window.matchMedia("(pointer:coarse)").matches)term.options.fontSize=11;
function pin(){if(!viewport)return;shell.style.top=viewport.offsetTop+"px";shell.style.left=viewport.offsetLeft+"px";shell.style.width=viewport.width+"px";shell.style.height=viewport.height+"px"}
function send(value){if(socket&&socket.readyState===1){socket.send(JSON.stringify(value));return true}return false}
function resize(){pin();try{fit.fit()}catch(_e){}send({type:"resize",cols:term.cols,rows:term.rows})}
function fitSoon(){resize();setTimeout(resize,60);setTimeout(resize,180);setTimeout(resize,420)}
function reconnect(){if(timer)clearTimeout(timer);timer=setTimeout(connect,Math.min(8000,400*Math.pow(2,retry++)))}
function connect(){var protocol=location.protocol==="https:"?"wss:":"ws:";socket=new WebSocket(protocol+"//"+location.host+"/ws");socket.onopen=function(){retry=0;overlay.classList.add("hidden");fitSoon()};socket.onmessage=function(event){term.write(event.data)};socket.onclose=function(){overlay.classList.remove("hidden");reconnect()};socket.onerror=function(){try{socket.close()}catch(_e){}}}
term.onData(function(data){send({type:"input",data:data})});term.onResize(function(size){send({type:"resize",cols:size.cols,rows:size.rows})});
if(term.parser&&term.parser.registerOscHandler){term.parser.registerOscHandler(52,function(data){var split=data.indexOf(";");if(split<0)return true;try{var decoded=atob(data.slice(split+1));if(navigator.clipboard&&navigator.clipboard.writeText)navigator.clipboard.writeText(decoded).catch(function(){})}catch(_e){}return true})}
document.getElementById("keys").addEventListener("click",function(event){var button=event.target.closest("button[data-key]");if(button)send({type:"input",data:button.getAttribute("data-key")})});
window.addEventListener("resize",fitSoon);window.addEventListener("orientationchange",fitSoon);if(viewport){viewport.addEventListener("resize",fitSoon);viewport.addEventListener("scroll",pin)}
if(window.ResizeObserver)new ResizeObserver(fitSoon).observe(shell);connect();fitSoon();term.focus();
})();`
