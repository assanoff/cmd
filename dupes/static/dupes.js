// dupes web: copying a path. A button with data-copy puts its value on the
// clipboard: with the Clipboard API on localhost, which browsers count as
// secure, and through a selection where they do not.
document.addEventListener("click", async (e) => {
	const b = e.target.closest("[data-copy]");
	if (!b) return;
	const text = b.dataset.copy;
	try {
		await navigator.clipboard.writeText(text);
	} catch {
		const t = document.createElement("textarea");
		t.value = text;
		document.body.append(t);
		t.select();
		document.execCommand("copy");
		t.remove();
	}
	b.classList.add("done");
	setTimeout(() => b.classList.remove("done"), 1200);
});
