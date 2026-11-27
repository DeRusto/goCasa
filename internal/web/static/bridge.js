// The Bridge to the Display app (gocasa-android), which injects
// window.gocasaDisplay before the page's scripts run. Without it this script
// does nothing. With it, after every load the page tells the app which screen
// it shows ({type: "state"}), a {type: "wake"} from the app's Camera wake
// touches #wake, and window.gocasaBridge tells the page's own wake lock and
// camera scripts that the app does their job. An app older than
// minimumVersion gets a notice and no Bridge, so the page runs as in a browser.
(() => {
	const minimumVersion = [0, 1, 0]; // the oldest Display app GoCasa supports
	const app = window.gocasaDisplay;
	if (!app) return;
	const tag = document.currentScript.dataset;
	const whenLoaded = f => document.readyState === "loading" ? document.addEventListener("DOMContentLoaded", f) : f();

	// supported is whether version, "X.Y.Z" (any suffix ignored), is at least minimumVersion.
	const supported = version => {
		const v = /^(\d+)\.(\d+)\.(\d+)/.exec(typeof version === "string" ? version : "");
		if (!v) return false;
		for (let i = 0; i < 3; i++) {
			const d = Number(v[i + 1]) - minimumVersion[i];
			if (d) return d > 0;
		}
		return true;
	};
	if (!supported(app.version)) {
		whenLoaded(() => {
			const notice = document.createElement("p");
			notice.setAttribute("role", "status");
			notice.textContent = "Update the Display app";
			Object.assign(notice.style, {
				position: "fixed", left: "50%", bottom: "5rem", whiteSpace: "nowrap", // above the phone tab bar
				transform: "translateX(-50%)", zIndex: "50", margin: "0",
				padding: ".5rem 1rem", borderRadius: "999px", background: "rgba(20,20,20,.7)", color: "#ddd",
				font: "1rem system-ui,sans-serif", pointerEvents: "none", // never in the way of a touch
			});
			document.body.append(notice);
		});
		return;
	}
	window.gocasaBridge = true;

	// message is what onmessage is given: the message, or an event whose data
	// is the message or its JSON text. Anything else is null.
	const message = m => {
		if (m && typeof m === "object" && "data" in m && !("type" in m)) m = m.data;
		if (typeof m === "string") {
			try { m = JSON.parse(m); } catch { return null; }
		}
		return m && typeof m === "object" ? m : null;
	};
	// Every message has a type; unknown types are ignored.
	app.onmessage = e => {
		const m = message(e);
		if (m && m.type === "wake") document.getElementById("wake")?.click(); // as a touch on the screensaver or Night page
	};

	whenLoaded(() => app.send({ type: "state", screen: tag.screen, cameraWake: tag.cameraWake === "true" }));
})();
