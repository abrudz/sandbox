// The interpreter page: TIO's frontend (github.com/TryItOnline/tryitonline,
// MIT) reduced to apl-dyalog and talking JSON to /api/v1/run. Permalink and
// output helpers are in codec.js; lb.js provides the APL language bar.

var bodyWidth = document.body.clientWidth;
var defaultLanguage = "apl-dyalog";
var languageId;
var languages;
var languagesURL = "/languages.json";
var navHeight = 48;
var runOnLoad = true;
var rEscapees = /[\x00-\x09\x0b-\x1f\x7f-\x9f&<>]| $/gm;
var rNewLine = /^/gm;
var rLineOfSpaces = /^\s+$/m;
var rSurroundingLinefeed = /^\n|\n$/;
var rUnpairedSurrogates = /[\ud800-\udbff](?![\udc00-\udfff])|([^\ud800-\udbff]|^)[\udc00-\udfff]/;
var rUnicodeCharacters = /[^][\udc00-\udfff]?/g;
var rUnprintable = /[\x00-\x09\x0b-\x1f\x7f-\x9f]/;
var rXxdLastLine = /(\w+):(.*?)\s\s.*$/;
var runController;
var runURL = "/api/v1/run";
var savedFocus;
var touchDevice = navigator.MaxTouchPoints > 0 || window.ontouchstart !== undefined;
var versionURL = "/api/v1/version";

// Signal numbers for TIO's "Exit code" line, which reported a signal N as
// status 128+N.
var signalNumbers = {
	SIGHUP: 1, SIGINT: 2, SIGQUIT: 3, SIGILL: 4, SIGTRAP: 5, SIGABRT: 6, SIGBUS: 7, SIGFPE: 8, SIGKILL: 9,
	SIGUSR1: 10, SIGSEGV: 11, SIGUSR2: 12, SIGPIPE: 13, SIGALRM: 14, SIGTERM: 15, SIGXCPU: 24, SIGXFSZ: 25, SIGSYS: 31
};

function $(selector, parent) {
	return (parent || document).querySelector(selector);
}

function $$(selector, parent) {
	return (parent || document).querySelectorAll(selector);
}

function iterate(iterable, monad) {
	if (!iterable)
		return;
	for (var i = 0; i < iterable.length; i++)
		monad(iterable[i]);
}

function sendMessage(title, text) {
	var message = clone("#templates > .message");

	$("h4", message).textContent = title;
	$("div", message).textContent = text;
	$("#messages").appendChild(message);
}

function resize(textArea) {
	var dummy = $("#dummy");
	textArea = this == window ? textArea : this;
	dummy.style.fontFamily = getComputedStyle(textArea).fontFamily;
	dummy.style.width = getComputedStyle(textArea).width;
	dummy.value = textArea.value;
	textArea.style.height = Math.max(dummy.scrollHeight, textArea.dataset.baseHeight || 27) + "px";
	dummy.value = "";
}

function addField(element) {
	var cla = clone("#templates .array");
	var textArea = $("textarea", cla);
	var parent = element.parentNode;
	parent.parentNode.insertBefore(cla, parent);
	textArea.onfocus = textArea.oninput = resize;
	if (!touchDevice)
		textArea.focus();
	return cla;
}

// runRequestBody returns the run request for the page's fields. Header,
// code and footer are joined as TIO joined them.
function runRequestBody() {
	var code = ($("#header").value && $("#header").value + "\n") + $("#code").value + ($("#footer").value && "\n" + $("#footer").value);
	var args = [];
	iterate($$("#cla-wrapper textarea"), function(textArea) { args.push(textArea.value); });
	return JSON.stringify({code: code, input: $("#input").value, args: args});
}

// timingLines returns what TIO appended to the debug output: bash's time
// report and the exit code.
function timingLines(result) {
	var real = result.wallTimeMs / 1000;
	var user = result.userTimeMs / 1000;
	var sys = result.sysTimeMs / 1000;
	var share = real > 0 ? (user + sys) / real * 100 : 0;
	var status;
	if (result.exitCode !== null)
		status = "Exit code: " + result.exitCode;
	else if (result.signal in signalNumbers)
		status = "Exit code: " + (128 + signalNumbers[result.signal]);
	else
		status = "Signal: " + result.signal;
	return "\nReal time: " + real.toFixed(3) + " s" +
		"\nUser time: " + user.toFixed(3) + " s" +
		"\nSys. time: " + sys.toFixed(3) + " s" +
		"\nCPU share: " + share.toFixed(2) + " %" +
		"\n" + status;
}

function showResult(result) {
	if (result.stdoutTruncated)
		sendMessage("Warning", "The output exceeded the limit and was truncated.");
	if (result.stderrTruncated)
		sendMessage("Warning", "The debugging information exceeded the limit and was truncated.");
	if (result.timedOut)
		sendMessage("Warning", "The request exceeded the time limit and was terminated.");
	$("#output").value = displayText(result.stdout);
	$("#debug").value = displayText(result.stderr) + timingLines(result);
	resize($("#output"));
	resize($("#debug"));
}

// The Run button shows the logo, the Dyalog D with a play triangle. While a
// run is in progress the D turns into a spinning cogwheel, as TIO's ring
// did, and back into the D afterwards. Both outlines are described around
// the centre of the logo's 224 by 247 box: for each of logoDirections
// directions, the distance to the inner and to the outer edge. The D's
// distances are measured on its path. On the D's open left side the two
// edges coincide, so that gap closes as the D turns into the cogwheel's
// ring. While the outline is not the D's, #run-morph draws it in place of
// #run-d, so the button at rest is exactly the logo.
var logoCentreX = 112;
var logoCentreY = 123.5;
var logoDirections = 360;
var logoEdges;
var gearTeeth = 8;
var gearTip = 110;
var gearBody = 86;
var gearHole = 62;
var gearToothTip = Math.PI / 20;
var gearToothBase = 7 * Math.PI / 90;
var gearTurnMs = 8000;
var morphMs = 800;
// The triangle's centre in the D, and how far it shrinks to fit the hole.
var playCentreX = 96.6;
var playCentreY = 123;
var playShrink = 0.8;
var morphTarget = 0;
var morphProgress = 0;
var gearAngle = 0;
var morphFrame;
var morphTime;

// measureLogo samples the D's outline as a polygon and measures, in each
// direction from the centre, the nearest and farthest crossing of it.
function measureLogo() {
	var path = $("#run-d");
	var length = path.getTotalLength();
	var points = [];
	for (var i = 0; i < 720; i++) {
		var point = path.getPointAtLength(length * i / 720);
		points.push([point.x - logoCentreX, point.y - logoCentreY]);
	}
	logoEdges = [];
	for (var k = 0; k < logoDirections; k++) {
		var angle = 2 * Math.PI * k / logoDirections;
		var dx = Math.cos(angle), dy = Math.sin(angle);
		var near = Infinity, far = 0;
		for (var j = 0; j < points.length; j++) {
			var p = points[j], q = points[(j + 1) % points.length];
			var ex = q[0] - p[0], ey = q[1] - p[1];
			var denominator = dx * ey - dy * ex;
			if (denominator == 0)
				continue;
			var t = (p[0] * ey - p[1] * ex) / denominator;
			var s = (p[0] * dy - p[1] * dx) / denominator;
			if (t > 0 && s >= 0 && s < 1) {
				near = Math.min(near, t);
				far = Math.max(far, t);
			}
		}
		// A direction through the open left side meets no edge; both
		// edges are then on the line the D's left side lies on.
		if (far == 0)
			near = far = -logoCentreX / dx;
		logoEdges.push([near, far]);
	}
}

// gearRadius returns the cogwheel's outer radius in the direction angle,
// measured from the centre of a tooth.
function gearRadius(angle) {
	var period = 2 * Math.PI / gearTeeth;
	var offset = Math.abs(((angle % period) + 1.5 * period) % period - period / 2);
	if (offset <= gearToothTip)
		return gearTip;
	if (offset >= gearToothBase)
		return gearBody;
	return gearBody + (gearTip - gearBody) * (gearToothBase - offset) / (gearToothBase - gearToothTip);
}

function point(radius, angle) {
	return (logoCentreX + radius * Math.cos(angle)).toFixed(2) + " " + (logoCentreY + radius * Math.sin(angle)).toFixed(2);
}

// drawLogo draws the outline for the current progress: 0 is the D and 1 the
// cogwheel, eased at both ends.
function drawLogo() {
	var t = morphProgress * morphProgress * (3 - 2 * morphProgress);
	var d = $("#run-d"), morph = $("#run-morph"), play = $("#run-play");
	if (t == 0) {
		d.style.display = "";
		morph.style.display = "none";
		play.removeAttribute("transform");
		return;
	}
	var outer = [], inner = [];
	for (var k = 0; k < logoDirections; k++) {
		var angle = 2 * Math.PI * k / logoDirections;
		var edges = logoEdges[k];
		// The teeth grow more slowly than the ring forms, so they rise out
		// of a ring rather than out of the D's outline.
		outer.push(point(edges[1] + t * (gearBody - edges[1]) + t * t * (gearRadius(angle - gearAngle) - gearBody), angle));
		inner.push(point(edges[0] + t * (gearHole - edges[0]), angle));
	}
	// The inner edge runs the other way round, which makes it a hole.
	morph.setAttribute("d", "M" + outer.join("L") + "Z M" + inner.reverse().join("L") + "Z");
	d.style.display = "none";
	morph.style.display = "";
	var scale = 1 - t * (1 - playShrink);
	var x = playCentreX + t * (logoCentreX - playCentreX), y = playCentreY + t * (logoCentreY - playCentreY);
	play.setAttribute("transform", "translate(" + x + " " + y + ") scale(" + scale + ") translate(" + -playCentreX + " " + -playCentreY + ")");
}

function animateLogo(now) {
	var elapsed = now - morphTime;
	morphTime = now;
	var step = elapsed / morphMs;
	morphProgress = morphTarget > morphProgress ? Math.min(morphTarget, morphProgress + step) : Math.max(morphTarget, morphProgress - step);
	gearAngle = (gearAngle + 2 * Math.PI * elapsed / gearTurnMs) % (2 * Math.PI);
	drawLogo();
	morphFrame = morphProgress == 0 && morphTarget == 0 ? undefined : requestAnimationFrame(animateLogo);
}

// setRunning marks the Run button as running or not, and turns its logo
// into the cogwheel or back. With reduced motion requested the change is
// immediate and the cogwheel stands still.
function setRunning(running) {
	$("#run").classList.toggle("running", running);
	if (!logoEdges)
		measureLogo();
	morphTarget = running ? 1 : 0;
	if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
		if (morphFrame)
			cancelAnimationFrame(morphFrame);
		morphFrame = undefined;
		morphProgress = morphTarget;
		drawLogo();
	} else if (!morphFrame) {
		morphTime = performance.now();
		morphFrame = requestAnimationFrame(animateLogo);
	}
}

// run sends the program to the run API. While a run is in progress, the
// Run button cancels it: aborting the request makes the server stop it.
function run() {
	if (runController) {
		runController.abort();
		return;
	}
	clearMessages();
	setRunning(true);
	var controller = runController = new AbortController();
	fetch(runURL, {
		method: "POST",
		headers: {"Content-Type": "application/json"},
		body: runRequestBody(),
		signal: controller.signal
	}).then(function(response) {
		return response.text().then(function(body) {
			if (response.status != 200)
				return sendMessage("Error " + response.status, body.trim() || response.statusText);
			showResult(JSON.parse(body));
		});
	}).catch(function(error) {
		if (error.name != "AbortError")
			sendMessage("Error", "Could not establish or maintain a connection with the server.");
	}).then(function() {
		if (runController === controller)
			runController = undefined;
		setRunning(false);
	});
}

function pluralization(number, string) {
	return number + " " + string + (number == 1 ? "" : "s");
}

function textToTextArea(text, textArea) {
	textArea.value = text;
	resize(textArea);
}

function fieldArrayToState(fieldArray, target) {
	var checkbox = $("input[type=checkbox]", target);
	if (checkbox !== null)
		checkbox.checked = true;
	iterate(fieldArray, function(field) {
		textToTextArea(field, $("textarea", addField($(".array-tail", target))));
	});
}

function clearState() {
	iterate($$("textarea"), function(textArea) {
		textArea.value = "";
		resize(textArea);
	});
	iterate($$(".array-remove", $("#interpreter")), function(element) {
		element.click();
	});
}

function hashToState(hash) {
	if (/=/.test(hash)) {
		// TIO's first permalink format: language#code=…&input=…&args=…
		clearState();
		var hashArray = hash.split("#");
		languageId = hashArray[0];
		var fields = hashArray[1].split("&");
		for (var i = 0; i < fields.length; i++) {
			var field = fields[i].split("=");
			if (field[0] == "args") {
				fieldArrayToState(field[1].split("+").map(function(arg) { return byteStringToText(base64ToByteString(arg)); }), $("#cla-wrapper"));
				continue;
			}
			if (field[1] && field[0] !== "debug") {
				var element = $("#" + field[0]);
				if (element === null)
					continue;
				textToTextArea(byteStringToText(base64ToByteString(field[1])), element);
			}
		}
		return true;
	}
	try {
		var permalink = parsePermalink(hash);
		languageId = permalink.language;
		if (!permalink.fields)
			return true;
		var fieldArray = permalink.fields;
		clearState();
		textToTextArea(fieldArray[1] || "", $("#header"));
		textToTextArea(fieldArray[2] || "", $("#code"));
		textToTextArea(fieldArray[3] || "", $("#footer"));
		textToTextArea(fieldArray[4] || "", $("#input"));
		fieldArrayToState(fieldArray.slice(5), $("#cla-wrapper"));
	} catch(error) {
		console.error(error);
		sendMessage("Error", "The permalink could not be decoded.");
		$("#toggle-index").checked = true;
		return false;
	}
	return true;
}

function countBytes(string, encoding) {
	if (string === "")
		return 0;
	if (encoding == "SBCS")
		return string.match(rUnicodeCharacters).length;
	if (encoding == "UTF-8")
		return textToByteString(string).length;
	if (encoding == "nibbles")
		return Math.ceil(string.match(rUnicodeCharacters).length / 2);
	if (encoding == "xxd") {
		var fields = string.match(rXxdLastLine);
		if (!fields)
			return 0;
		return Number("0x" + fields[1]) + fields[2].match(/\S\S/g).length;
	}
}

function clearMessages() {
	iterate($$(".message"), function(element) {
		if (element.textContent !== "placeholder")
			element.click();
	});
}

function postStateFill() {
	$("#toggle-header").checked    = $("#header").value            !== "";
	$("#toggle-footer").checked    = $("#footer").value            !== "";
	$("#toggle-input").checked     = $("#input").value             !== "";
	$("#toggle-arguments").checked = $("#cla-wrapper textarea")    !== null;
	$("#code").oninput();
	clearMessages();
	if (!touchDevice)
		setTimeout(function() { $("#code").focus(); }, 10);
}

function testToState(test) {
	saveState();
	clearState();
	iterate(languages[languageId].tests[test].request, function(instruction) {
		for (var key in instruction.payload)
			var name = key, value = instruction.payload[key];
		if (instruction.command == "F")
			$(name == ".code.tio" ? "#code" : "#input").value = value;
		else if (instruction.command == "V")
			fieldArrayToState(value, $("#cla-wrapper"));
	});
	saveState(true);
	postStateFill();
}

// init shows the state in the URL's fragment. On the first load, a
// permalink with a program runs at once; going back and forward in the
// history does not run anything.
function init() {
	var runProgram = runOnLoad;
	runOnLoad = false;
	$("#toggle-permalink").checked = false;
	$("nav").classList.remove("hidden");
	var hash = unescape(location.hash.slice(1));
	// Both permalink formats put the state after a "#"; a bare language
	// has none.
	var permalink = hash.indexOf("#") >= 0;
	// tio.run's own pages: an empty fragment, its home page and its language list.
	if (/^(|community|home|get-started)$/.test(hash))
		hash = defaultLanguage;
	if (!hashToState(hash))
		return;
	var language = languages[languageId];
	if (!language) {
		$("#toggle-index").checked = true;
		sendMessage("Error", "The requested language could not be found. This may be due to caching; try a hard refresh.");
		languageId = undefined;
		return;
	}
	$("#toggle-interpreter").checked = true;
	$("#lang-link").href = language.link;
	$("#lang-name").textContent = language.name;
	postStateFill();
	scrollTo(0, 0);
	if (runProgram && permalink && ($("#header").value || $("#code").value || $("#footer").value))
		run();
}

function clone(queryString) {
	return $(queryString).cloneNode(true);
}

function copyToClipboard() {
	$("textarea", this.parentNode.parentNode).select();
	document.execCommand("copy");
}

function codeToMarkdown(code) {
	if (code === "")
		return "<pre><code></code></pre>";
	if (rLineOfSpaces.test(code) || rSurroundingLinefeed.test(code) || rUnprintable.test(code))
		return "<pre><code>" + code.replace(rEscapees, function(character) {
			switch (character) {
				case "\0": return "";
				case "<":  return "&lt;";
				case ">":  return "&gt;";
				case "&":  return "&amp;";
				default:   return "&#" + character.charCodeAt(0) + ";";
			}
		}) + "\n</code></pre>";
	else
		return code.replace(rNewLine, "    ");
}

function saveState(saveIfEmpty) {
	if (!languageId)
		return;
	var fields = [languageId, $("#header").value, $("#code").value, $("#footer").value, $("#input").value];
	iterate($$("#cla-wrapper textarea"), function(textArea) { fields.push(textArea.value); });
	var empty = fields.slice(1).every(function(field) { return field === ""; });
	if (saveIfEmpty || !empty)
		history.pushState({}, "", fieldsToPermalink(fields));
}

// layoutLanguageBar places the navigation bar under lb.js's language bar,
// which is fixed at the top of the window, and the page under both. lb.js
// also sets a top margin on the body, which TIO's fixed layout does not use.
// lb.js loads before this script, so the bar exists when boot runs.
function layoutLanguageBar() {
	var bar = $(".ngn_lb");
	var height = bar ? bar.offsetHeight : 0;
	$("nav").style.top = height + "px";
	$("#main").style.top = height + navHeight + "px";
}

// refreshActiveField updates the byte count and size of the focused field
// after lb.js inserts a glyph, which it does without an input event.
function refreshActiveField() {
	var element = document.activeElement;
	if (element && element.tagName == "TEXTAREA" && element.oninput)
		element.oninput();
}

function boot() {
	iterate($$("h3 label span"), function(element) {
		var svg = clone("#templates > .bullet");
		element.parentNode.insertBefore(svg, element);
	});

	iterate($$(".copy-entry"), function(element) {
		var copyButton = $(".copy-button", element);
		copyButton.title = "Copy to clipboard and close the drawer.";
		copyButton.onclick = copyToClipboard;
		copyButton.appendChild(clone("#templates .copy-icon"));
	});

	iterate($$("textarea:not([id=dummy]), input[type=text]"), function(element) {
		element.spellcheck = false;
		element.setAttribute("autocapitalize", "none");
		element.setAttribute("autocorrect", "off");
		if (element.tagName !== "TEXTAREA")
			return;
		element.onfocus = element.oninput = resize;
		resize(element);
	});

	addEventListener("resize", function() {
		layoutLanguageBar();
		if (document.body.clientWidth == bodyWidth)
			return;
		bodyWidth = document.body.clientWidth;
		var textAreas = $$("#interpreter textarea");
		for (var i = 0; i < textAreas.length; i++)
			resize(textAreas[i]);
	});

	$("#run").onclick = run;

	$("#permalink").onclick = function() {
		var code = $("#code").value;
		var language = languages[languageId];
		saveState(true);
		var data = {
			"bytes": pluralization(countBytes(code, language.encoding), "byte"),
			"markdownCode": codeToMarkdown(code),
			"prettifyHint": language.prettify ? "<!-- language-all: lang-" + language.prettify + " -->\n\n" : "",
			"lang": language.name,
			"link": language.link,
			"n": "\n",
			"nn": "\n\n",
			"permalink": location.href,
			"timestamp": Date.now().toString(36)
		};
		var textAreas = $$("#permalink-drawer textarea");
		for (var i = 0; i < textAreas.length; i++) {
			var textArea = textAreas[i];
			textArea.style.height = textArea.dataset.baseHeight + "px";
			textArea.value = textArea.dataset.format.replace(/\{\{(\w*)\}\}/g, function(_, match) {
				return data[match];
			});
		}
	};

	$("#lang-example").onclick = function() { testToState("helloWorld"); };

	$("#code").oninput = function() {
		var code = $("#code").value;
		var encoding = languages[languageId].encoding;

		resize($("#code"));

		if (rUnpairedSurrogates.test(code))
			return $("#code-info").textContent = "invalid Unicode: unpaired surrogates";

		var characterCount = countBytes(code, "SBCS");
		var byteCount = countBytes(code, encoding);

		$("#code-info").textContent  = pluralization(characterCount, "char");
		$("#code-info").textContent += ", " + pluralization(byteCount, "byte") + " (" + encoding + ")";
	};

	fetch(versionURL).then(function(response) {
		return response.json();
	}).then(function(version) {
		$("#lang-version").textContent = version.dyalogVersion;
	}).catch(function(error) {
		console.error(error);
	});

	function modifiers(event) {
		return event.altKey << 3 | event.ctrlKey << 2 | event.metaKey << 1 | event.shiftKey;
	}

	function typeString(string) {
		var element = document.activeElement;

		if (document.execCommand("insertText", false, string) == false) {
			if (element.selectionStart === undefined)
				return;
			document.execCommand("ms-beginUndoUnit");
			var start = element.selectionStart;
			var end = element.selectionEnd;
			element.value = element.value.slice(0, start) + string + element.value.slice(end);
			element.selectionStart = start + string.length;
			element.selectionEnd = element.selectionStart;
			document.execCommand("ms-endUndoUnit");
		}

		element.oninput();
	}

	function advanceFocus() {
		var focused = $("textarea:focus");
		var focusable = $$("input:checked + h3 + textarea, input:checked + h3 + div textarea");
		if (focused == null)
			return;
		else {
			for (var index = 0; focusable[index] != focused && index < focusable.length; index++);
			focusable[(index + 1) % focusable.length].focus();
		}
	}

	addEventListener("popstate", init);
	addEventListener("beforeunload", function() { saveState(); });
	addEventListener("keyup", refreshActiveField);
	addEventListener("mouseup", refreshActiveField);

	function toggleCommandMode(event) {
		$("body").classList.toggle("command-mode");
		if ($("body").classList.contains("command-mode"))
			savedFocus = event.target;
		else
			savedFocus.focus();
	}

	addEventListener("keydown", function(event) {
		if (modifiers(event) == 0 && event.keyCode == 27) {
			event.preventDefault();
			toggleCommandMode(event);
		}
		else if ($("body").classList.contains("command-mode")) {
			event.preventDefault();
			var key = String.fromCharCode(event.keyCode & 95);
			var element = "A" <= key && key <= "Z" && $("[data-hotkey=" + key + "]");
			if (modifiers(event))
				return;
			if (element && element.offsetParent !== null) {
				if (element !== $("#permalink") || $("#toggle-permalink").checked)
					toggleCommandMode();
				element.click();
			}
		}
		else if (modifiers(event) == 0 && event.keyCode == 9) {
			// lb.js completes glyph sequences on Tab and cancels the event.
			if (event.defaultPrevented)
				return;
			event.preventDefault();
			if (event.target.classList.contains("read-only") == false)
				typeString(languages[languageId].tab || "\t", event);
		}
		else if (modifiers(event) == 1 && event.keyCode == 9) {
			event.preventDefault();
			advanceFocus();
		}
		else if (modifiers(event) == 4 && event.keyCode == 13) {
			event.preventDefault();
			$("#run").click();
		}
	});

	// The bar's height changes as its font loads and as the window narrows.
	var bar = $(".ngn_lb");
	if (bar && window.ResizeObserver)
		new ResizeObserver(layoutLanguageBar).observe(bar);
	layoutLanguageBar();
}

fetch(languagesURL).then(function(response) {
	return response.json();
}).then(function(json) {
	languages = json;
	boot();
	init();
}).catch(function(error) {
	console.error(error);
	sendMessage("Error", "Some resources could not be loaded. Please refresh the page and try again.");
});
