package dom

type HelperName string

const (
	HelperResolve       HelperName = "resolve"
	HelperCount         HelperName = "count"
	HelperMatchesState  HelperName = "matchesState"
	HelperActionability HelperName = "actionability"

	HelperFillElementValue HelperName = "fillElementValue"
	HelperSetChecked       HelperName = "setChecked"
	HelperSelectOptions    HelperName = "selectOptions"
	HelperScrollToPercent  HelperName = "scrollToPercent"
	HelperFocusElement     HelperName = "focusElement"
	HelperGetInputValue    HelperName = "getInputValue"
	HelperGetTextContent   HelperName = "getTextContent"
	HelperGetInnerHTML     HelperName = "getInnerHTML"
	HelperGetInnerText     HelperName = "getInnerText"
	HelperIsChecked        HelperName = "isChecked"
)

type Helper struct {
	Source string
	Args   []string
}

var helperSources = map[HelperName]string{
	HelperFillElementValue: `(function(value) {
	this.focus();
	this.scrollIntoView({ block: "center", inline: "center" });
	if (this.isContentEditable) {
		this.textContent = value;
	} else if ("value" in this) {
		this.value = value;
	}
	this.dispatchEvent(new Event("input", { bubbles: true }));
	this.dispatchEvent(new Event("change", { bubbles: true }));
	return true;
})`,

	HelperSetChecked: `(function(checked) {
	this.scrollIntoView({ block: "center", inline: "center" });
	const targetChecked = !!checked;
	if (!!this.checked !== targetChecked) {
		this.click();
	}
	if (!!this.checked !== targetChecked) {
		this.checked = targetChecked;
		this.dispatchEvent(new Event("input", { bubbles: true }));
		this.dispatchEvent(new Event("change", { bubbles: true }));
	}
	return !!this.checked === targetChecked;
})`,

	HelperSelectOptions: `(function(values) {
	const wanted = new Set(values);
	for (const option of this.options || []) {
		option.selected = wanted.has(option.value) || wanted.has(option.text);
	}
	this.dispatchEvent(new Event("input", { bubbles: true }));
	this.dispatchEvent(new Event("change", { bubbles: true }));
	return true;
})`,

	HelperScrollToPercent: `(function(percent) {
	const p = Math.max(0, Math.min(100, percent));
	this.scrollIntoView({ block: "center", inline: "center" });
	this.scrollTop = (this.scrollHeight - this.clientHeight) * (p / 100);
	return true;
})`,

	HelperFocusElement: `(function() {
	this.focus();
	return true;
})`,

	HelperGetInputValue: `(function() {
	return "value" in this ? String(this.value ?? "") : "";
})`,

	HelperGetTextContent: `(function() {
	return this.textContent || "";
})`,

	HelperGetInnerHTML: `(function() {
	return this.innerHTML || "";
})`,

	HelperGetInnerText: `(function() {
	return this.innerText || "";
})`,

	HelperIsChecked: `(function() {
	return !!this.checked;
})`,
}

func GetFunctionSource(name HelperName) string {
	return helperSources[name]
}

func BuildInvocation(name HelperName, args ...string) string {
	return BuildInvocationWithHelper(name, args...)
}

func BuildInvocationWithHelper(name HelperName, args ...string) string {
	argsStr := ""
	for i, arg := range args {
		if i > 0 {
			argsStr += ", "
		}
		argsStr += arg
	}
	return `(() => { ` + BootstrapScript() + `; return globalThis.__polymuxSelectorHelper.` + string(name) + `(` + argsStr + `); })()`
}
