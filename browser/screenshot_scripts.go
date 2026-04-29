package browser

const resolveMaskRectScript = `function(maskToken) {
	function safeClosest(el, selector) {
		try {
			return el && typeof el.closest === 'function' ? el.closest(selector) : null;
		} catch {
			return null;
		}
	}
	function safeMatches(el, selector) {
		try {
			return !!el && typeof el.matches === 'function' && el.matches(selector);
		} catch {
			return false;
		}
	}
	function findTopLayerRoot(el) {
		const dialog = safeClosest(el, 'dialog[open]');
		if (dialog) return dialog;
		const popover = safeClosest(el, '[popover]');
		if (popover && safeMatches(popover, ':popover-open')) return popover;
		return null;
	}
	if (!this || typeof this.getBoundingClientRect !== 'function') return null;
	const rect = this.getBoundingClientRect();
	if (!rect) return null;
	const style = window.getComputedStyle(this);
	if (!style) return null;
	if (style.visibility === 'hidden' || style.display === 'none') return null;
	if (rect.width <= 0 || rect.height <= 0) return null;
	const root = findTopLayerRoot(this);
	if (root) {
		const rootRect = root.getBoundingClientRect();
		if (!rootRect) return null;
		let rootToken = null;
		if (maskToken) {
			try {
				const existing = root.getAttribute('data-polymux-mask-root');
				if (existing && existing.startsWith(maskToken)) {
					rootToken = existing;
				} else {
					rootToken = maskToken + '_root_' + Math.random().toString(36).slice(2);
					root.setAttribute('data-polymux-mask-root', rootToken);
				}
			} catch {
				rootToken = null;
			}
		}
		return {
			x: rect.left - rootRect.left - (root.clientLeft || 0) + (root.scrollLeft || 0),
			y: rect.top - rootRect.top - (root.clientTop || 0) + (root.scrollTop || 0),
			width: rect.width,
			height: rect.height,
			rootToken: rootToken
		};
	}
	return {
		x: rect.left + window.scrollX,
		y: rect.top + window.scrollY,
		width: rect.width,
		height: rect.height,
		rootToken: null
	};
}`
