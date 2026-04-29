package browser

import (
	"context"
	"sync"
)

const v3PiercerScript = `(function(){
var opts=arguments[0]||{};
var DEBUG=opts.debug!==false;
var tagExisting=opts.tagExisting===true;
var hostToRoot=new WeakMap();
var openCount=0;
var closedCount=0;
var currentFn=Element.prototype.attachShadow;
if(currentFn.__v3Patched&&currentFn.__v3State){
	currentFn.__v3State.debug=DEBUG;
	bindBackdoor(currentFn.__v3State);
	return;
}
var state={hostToRoot:hostToRoot,openCount:openCount,closedCount:closedCount,debug:DEBUG};
var original=currentFn;
var patched=function(init){
	var mode=init&&init.mode?init.mode:"open";
	var root=original.call(this,init);
	try{
		state.hostToRoot.set(this,root);
		if(mode==="closed"){
			state.closedCount++;
		}else{
			state.openCount++;
		}
		if(state.debug){
			console.info("[v3-piercer] attachShadow",{tag:this.tagName?this.tagName.toLowerCase():"",mode:mode,url:location.href});
		}
	}catch(e){}
	return root;
};
patched.__v3Patched=true;
patched.__v3State=state;
Object.defineProperty(Element.prototype,"attachShadow",{configurable:true,writable:true,value:patched});
if(tagExisting){
	try{
		var walker=document.createTreeWalker(document,NodeFilter.SHOW_ELEMENT);
		while(walker.nextNode()){
			var el=walker.currentNode;
			if(el.shadowRoot){
				state.hostToRoot.set(el,el.shadowRoot);
				state.openCount++;
			}
		}
	}catch(e){}
}
window.__stagehandV3Injected=true;
bindBackdoor(state);
if(state.debug){
	console.info("[v3-piercer] installed",{url:location.href,isTop:window.top===window,readyState:document.readyState});
}
function bindBackdoor(s){
	window.__stagehandV3__={
		getClosedRoot:function(host){
			return s.hostToRoot.get(host);
		},
		stats:function(){
			return{installed:true,url:location.href,isTop:window.top===window,open:s.openCount,closed:s.closedCount};
		}
	};
}
})({debug:true,tagExisting:false});`

const rerenderMissingShadowsScript = `(function(){
try{
	var piercer=window.__stagehandV3__;
	if(!piercer||typeof piercer.getClosedRoot!=="function")return;
	var needsReset=[];
	var walker=document.createTreeWalker(document,NodeFilter.SHOW_ELEMENT);
	while(walker.nextNode()){
		var el=walker.currentNode;
		var tag=el.tagName?el.tagName.toLowerCase():"";
		if(!tag.includes("-"))continue;
		if(typeof customElements==="undefined"||typeof customElements.get!=="function")continue;
		if(!customElements.get(tag))continue;
		var hasOpen=!!el.shadowRoot;
		var hasClosed=!!piercer.getClosedRoot(el);
		if(hasOpen||hasClosed)continue;
		needsReset.push(el);
	}
	for(var i=0;i<needsReset.length;i++){
		try{
			var host=needsReset[i];
			var clone=host.cloneNode(true);
			host.parentNode.replaceChild(clone,host);
		}catch(e){}
	}
	if(piercer.stats&&needsReset.length>0){
		console.info("[v3-piercer] rerender",{count:needsReset.length});
	}
}catch(err){
	console.info("[v3-piercer] rerender error",{message:String(err||"")});
}
})();`

var piercerMu sync.Mutex
var piercerInstalled map[string]struct{}

func init() {
	piercerInstalled = make(map[string]struct{})
}

func InstallPiercer(ctx context.Context, session sessionLike) error {
	piercerMu.Lock()
	if _, ok := piercerInstalled[session.ID()]; ok {
		piercerMu.Unlock()
		return nil
	}
	piercerInstalled[session.ID()] = struct{}{}
	piercerMu.Unlock()

	_ = session.Send(ctx, "Page.enable", nil, nil)
	_ = session.Send(ctx, "Runtime.enable", nil, nil)

	err := session.Send(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source":         v3PiercerScript,
		"runImmediately": true,
	}, nil)
	if err != nil {
		return err
	}

	_ = session.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    v3PiercerScript,
		"returnByValue": true,
		"awaitPromise":  true,
	}, nil)

	_ = session.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    rerenderMissingShadowsScript,
		"returnByValue": true,
		"awaitPromise":  false,
	}, nil)

	return nil
}

type PiercerStats struct {
	Installed bool
	URL       string
	IsTop     bool
	Open      int
	Closed    int
}

func GetPiercerStats(ctx context.Context, session sessionLike) (*PiercerStats, error) {
	var res struct {
		Result struct {
			Value *struct {
				Installed bool   `json:"installed"`
				URL       string `json:"url"`
				IsTop     bool   `json:"isTop"`
				Open      int    `json:"open"`
				Closed    int    `json:"closed"`
			} `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	err := session.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    "window.__stagehandV3__ ? window.__stagehandV3__.stats() : null",
		"returnByValue": true,
		"awaitPromise":  false,
	}, &res)
	if err != nil {
		return nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, nil
	}
	if res.Result.Value == nil {
		return nil, nil
	}
	return &PiercerStats{
		Installed: res.Result.Value.Installed,
		URL:       res.Result.Value.URL,
		IsTop:     res.Result.Value.IsTop,
		Open:      res.Result.Value.Open,
		Closed:    res.Result.Value.Closed,
	}, nil
}
