package humanize

// PointerCoalescedScript returns a JavaScript snippet that patches
// PointerEvent.getCoalescedEvents() to return one to three synthetic
// intermediate events whenever the runtime would otherwise return a single
// (uncoalesced) event. Real pointer hardware fires several coalesced events
// per frame; CDP-synthesized pointer events fire only one, and detectors use
// the count as a behavioral signal.
//
// Inject once per page via Page.AddInitScript so the patch is in place before
// any site script runs:
//
//	page.AddInitScript(ctx, humanize.PointerCoalescedScript())
//
// The script is idempotent — it short-circuits on a window flag.
func PointerCoalescedScript() string {
	return `(() => {
    if (window.__cbHumanCoalesced) return;
    window.__cbHumanCoalesced = true;
    const original = PointerEvent.prototype.getCoalescedEvents;
    PointerEvent.prototype.getCoalescedEvents = function() {
        const real = original.call(this);
        if (real.length <= 1) {
            const count = 1 + Math.floor(Math.random() * 3);
            const fake = [this];
            for (let i = 0; i < count; i++) {
                fake.push(new PointerEvent(this.type, {
                    clientX: this.clientX + (Math.random() - 0.5) * 2,
                    clientY: this.clientY + (Math.random() - 0.5) * 2,
                    pointerId: this.pointerId,
                    pointerType: this.pointerType,
                    bubbles: false
                }));
            }
            return fake;
        }
        return real;
    };
})();`
}
