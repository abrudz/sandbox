// Permalink and output helpers shared by frontend.js and the Node tests
// (test/ui/codec.test.js). The byte-string, base64 and deflate helpers are
// TIO's (github.com/TryItOnline/tryitonline, MIT), so permalinks keep TIO's
// format and a tio.run link opens here once its host is replaced. Needs
// pako (pako.min.js) as a global.

var fieldSeparator = "\xff";
var rFieldString = /^[\x00-\xf3\xff]+/;

function byteStringToByteArray(byteString) {
	var byteArray = new Uint8Array(byteString.length);
	for (var index = 0; index < byteString.length; index++)
		byteArray[index] = byteString.charCodeAt(index);
	byteArray.head = 0;
	return byteArray;
}

function byteArrayToByteString(byteArray) {
	var retval = "";
	for (var index = 0; index < byteArray.length; index++)
		retval += String.fromCharCode(byteArray[index]);
	return retval;
}

function textToByteString(string) {
	return unescape(encodeURIComponent(string));
}

function byteStringToText(byteString) {
	return decodeURIComponent(escape(byteString));
}

function deflate(byteString) {
	return pako.deflateRaw(byteStringToByteArray(byteString), {"level": 9});
}

function inflate(byteString) {
	return byteArrayToByteString(pako.inflateRaw(byteString));
}

function byteStringToBase64(byteString) {
	return btoa(byteString).replace(/\+/g, "@").replace(/=+/, "");
}

function base64ToByteString(base64String) {
	return atob(unescape(base64String).replace(/@|-/g, "+").replace(/_/g, "/"));
}

// fieldsToPermalink returns the URL fragment, "##" and the encoded state,
// for fields: the language, header, code, footer, input and each argument.
function fieldsToPermalink(fields) {
	var stateString = fields.map(textToByteString).join(fieldSeparator);
	return "##" + byteStringToBase64(byteArrayToByteString(deflate(stateString)));
}

// parsePermalink reads a URL fragment without its leading "#": either a
// bare language, or an optional language, "#" and the encoded state. It
// returns the language and the fields in fieldsToPermalink's order, or
// null fields for a bare language. TIO's extra fields and settings, which
// apl-dyalog permalinks never carry, are ignored.
function parsePermalink(fragment) {
	var parts = fragment.split("#");
	if (parts.length < 2)
		return {language: parts[0].toLowerCase(), fields: null};
	var stateString = (parts[0] ? parts[0] + fieldSeparator : "") + inflate(base64ToByteString(parts[1]));
	var fields = stateString.match(rFieldString)[0].split(fieldSeparator).map(byteStringToText);
	fields[0] = fields[0].toLowerCase();
	return {language: fields[0], fields: fields};
}

// displayText returns program output with its line endings as line feeds:
// Dyalog ends output lines with CR in script mode.
function displayText(output) {
	return output.replace(/\r\n?/g, "\n");
}

if (typeof module !== "undefined")
	module.exports = {
		fieldsToPermalink: fieldsToPermalink,
		parsePermalink: parsePermalink,
		displayText: displayText,
	};
