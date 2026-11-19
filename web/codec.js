// Permalink and output helpers shared by frontend.js and the Node tests.

function fieldsToPermalink(fields) {
	throw new Error("not implemented");
}

function parsePermalink(fragment) {
	throw new Error("not implemented");
}

function displayText(output) {
	throw new Error("not implemented");
}

if (typeof module !== "undefined")
	module.exports = { fieldsToPermalink: fieldsToPermalink, parsePermalink: parsePermalink, displayText: displayText };
