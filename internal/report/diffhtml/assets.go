package diffhtml

const reviewStyles = `
:root {
	color-scheme: dark;
	font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
	--ink: #e7edf5;
	--muted: #a5b4c6;
	--quiet: #8190a3;
	--line: #26354a;
	--line-strong: #45617f;
	--paper: #07111d;
	--surface: #101b29;
	--surface-raised: #142133;
	--surface-cool: #17263a;
	--navy-deep: #f3f7fb;
	--rust: #d17342;
	--rust-bright: #f08a51;
	--teal: #69d7c2;
	--focus: #69b8ff;
	--code-bg: #050b12;
	--shadow: rgba(0, 0, 0, 0.32);
	background: var(--paper);
	color: var(--ink);
}
body {
	margin: 0;
	min-height: 100vh;
	background: linear-gradient(180deg, #07111d 0%, #0a1421 46%, #101827 100%);
	color: var(--ink);
	font-size: 14px;
	line-height: 1.45;
}
:focus-visible {
	outline: 3px solid var(--focus);
	outline-offset: 3px;
}
.report-header {
	position: sticky;
	top: 0;
	z-index: 5;
	display: grid;
	grid-template-columns: minmax(0, 1fr) auto;
	align-items: center;
	gap: 18px;
	padding: 12px 22px;
	border-bottom: 1px solid var(--line);
	background: rgba(8, 17, 29, 0.94);
	backdrop-filter: blur(10px);
}
.report-header h1 {
	margin: 0 0 6px;
	font-size: 20px;
	line-height: 1.25;
	color: var(--navy-deep);
}
.brand-logo {
	display: block;
	width: clamp(186px, 18vw, 210px);
	height: auto;
}
.summary, .resource-meta {
	margin: 0;
	color: var(--muted);
	font-size: 14px;
	line-height: 1.45;
}
.review-layout {
	display: grid;
	grid-template-columns: minmax(240px, 320px) minmax(0, 1fr);
	min-height: calc(100vh - 82px);
}
.tree {
	border-right: 1px solid var(--line);
	background: rgba(16, 27, 41, 0.86);
	padding: 16px;
	overflow: auto;
}
.tree-search {
	box-sizing: border-box;
	width: 100%;
	margin: 0 0 14px;
	padding: 9px 10px;
	border: 1px solid var(--line-strong);
	border-radius: 6px;
	background: rgba(5, 11, 18, 0.78);
	color: var(--ink);
	font: inherit;
}
.tree-search::placeholder {
	color: var(--quiet);
}
.tree h2 {
	margin: 16px 0 6px;
	font-size: 14px;
	line-height: 1.45;
	text-transform: uppercase;
	color: var(--quiet);
}
.tree-resource {
	display: grid;
	grid-template-columns: minmax(0, 1fr) auto;
	align-items: baseline;
	column-gap: 8px;
	width: 100%;
	margin: 3px 0;
	padding: 7px 8px;
	border: 0;
	border-radius: 6px;
	background: transparent;
	color: var(--ink);
	font: inherit;
	font-size: 14px;
	line-height: 1.45;
	text-align: left;
	cursor: pointer;
}
.tree-resource-label {
	min-width: 0;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}
.tree-delta {
	display: inline-flex;
	gap: 6px;
	align-items: baseline;
	font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
	font-size: 12px;
	font-variant-numeric: tabular-nums;
	line-height: 1;
	white-space: nowrap;
}
.tree-delta-added {
	color: #9ce8a8;
}
.tree-delta-removed {
	color: #ffb4ae;
}
.tree-resource[aria-current="true"] {
	background: rgba(105, 215, 194, 0.12);
	color: var(--navy-deep);
}
.review-main {
	min-width: 0;
	padding: 18px 22px 34px;
	overflow: auto;
}
.resource-header {
	position: sticky;
	top: 0;
	z-index: 3;
	display: grid;
	grid-template-columns: minmax(0, 1fr) auto;
	align-items: start;
	gap: 14px;
	margin: 0 0 8px;
	padding: 0 0 6px;
	background: linear-gradient(180deg, #0a1421 74%, rgba(10, 20, 33, 0));
}
.resource-title {
	min-width: 0;
}
.toolbar {
	display: flex;
	justify-content: flex-end;
	gap: 8px;
	margin: 0;
	padding: 0;
	white-space: nowrap;
}
.toolbar button {
	padding: 5px 9px;
	border: 1px solid var(--line-strong);
	border-radius: 6px;
	background: var(--surface-raised);
	color: var(--ink);
	font: inherit;
	font-size: 14px;
	line-height: 1.45;
	cursor: pointer;
}
.toolbar button[aria-pressed="true"] {
	background: rgba(105, 215, 194, 0.13);
	border-color: var(--teal);
	color: var(--navy-deep);
}
.applications {
	min-width: 0;
}
.resource {
	display: none;
	margin: 0;
}
.resource.is-active {
	display: block;
}
.resource h3 {
	margin: 0 0 4px;
	font-size: 18px;
	line-height: 1.25;
	color: var(--navy-deep);
}
.diff-table {
	width: 100%;
	border-collapse: collapse;
	margin-top: 0;
	background: var(--code-bg);
	border: 1px solid var(--line);
	font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
	font-size: 13px;
	line-height: 1.45;
}
.diff-table th {
	text-align: left;
}
.hunk-header th {
	padding: 5px 8px;
	background: rgba(105, 184, 255, 0.13);
	color: #8ec8ff;
	font-weight: 600;
}
.line-number {
	width: 1%;
	min-width: 44px;
	padding: 2px 8px;
	border-right: 1px solid var(--line);
	background: rgba(16, 27, 41, 0.8);
	color: var(--quiet);
	text-align: right;
	user-select: none;
	vertical-align: top;
}
.line-code {
	padding: 2px 8px;
	white-space: pre-wrap;
	word-break: break-word;
	vertical-align: top;
}
.diff-row.added .line-code {
	background: rgba(63, 185, 80, 0.13);
}
.diff-row.added .line-number {
	background: rgba(63, 185, 80, 0.14);
	color: #9ce8a8;
}
.diff-row.removed .line-code {
	background: rgba(248, 81, 73, 0.13);
}
.diff-row.removed .line-number {
	background: rgba(248, 81, 73, 0.14);
	color: #ffb4ae;
}
.diff-table.one-sided .line-code {
	width: 100%;
}
.inline-change {
	border-radius: 3px;
	padding: 0 1px;
}
.inline-change.added {
	background: rgba(63, 185, 80, 0.38);
	box-shadow: 0 0 0 1px rgba(63, 185, 80, 0.2);
}
.inline-change.removed {
	background: rgba(248, 81, 73, 0.38);
	box-shadow: 0 0 0 1px rgba(248, 81, 73, 0.2);
}
body[data-view="side-by-side"] .diff-table.unified,
body[data-view="unified"] .diff-table.side-by-side {
	display: none;
}
.diagnostics {
	margin: 28px 0 0;
	border: 1px solid rgba(209, 115, 66, 0.32);
	border-radius: 8px;
	background: rgba(209, 115, 66, 0.08);
	color: var(--muted);
}
.diagnostics summary {
	cursor: pointer;
	padding: 10px 12px;
	color: var(--rust-bright);
	font-weight: 700;
}
.diagnostics ul {
	margin: 0;
	padding: 0 14px 12px 30px;
}
.diagnostics li + li {
	margin-top: 6px;
}
.severity, .category {
	color: var(--navy-deep);
}
.no-diff {
	margin: 0;
	color: var(--muted);
}
@media (max-width: 800px) {
	.report-header {
		position: static;
		grid-template-columns: 1fr;
	}
	.brand-logo {
		width: 186px;
	}
	.review-layout {
		grid-template-columns: 1fr;
	}
	.tree {
		max-height: 280px;
		border-right: 0;
		border-bottom: 1px solid var(--line);
	}
	.resource-header {
		grid-template-columns: 1fr;
		gap: 8px;
	}
	.toolbar {
		justify-content: flex-start;
		white-space: normal;
	}
}
`

const drydockLogo = `<svg class="brand-logo" aria-label="drydock" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 480 128" width="480" height="128">
  <defs>
    <linearGradient id="hull-grad" x1="0" y1="0" x2="1" y2="0">
      <stop offset="0%" stop-color="#b95a2d"/>
      <stop offset="100%" stop-color="#e07a3f"/>
    </linearGradient>
  </defs>
  <path d="M16 58 L28 58 L34 102 L94 102 L100 58 L112 58 L106 110 L22 110 Z" fill="#d6e0eb"/>
  <rect x="59" y="90" width="10" height="12" fill="#8fa1b7"/>
  <path d="M35 32 L93 32 L93 58 C93 80 77 90 64 90 C51 90 35 80 35 58 Z" fill="url(#hull-grad)"/>
  <line x1="36" y1="58" x2="92" y2="58" stroke="#ffad73" stroke-width="1.5"/>
  <path d="M64 12 L87 22 L87 32 L41 32 L41 22 Z" fill="#e6a15f"/>
  <text x="144" y="76" font-family="Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif" font-size="52" font-weight="600" fill="#f3f7fb" letter-spacing="2">drydock</text>
</svg>`

const drydockFaviconHref = "data:image/svg+xml;base64," +
	"PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAxMjggMTI4IiB3aWR0aD0iMTI4IiBoZWlnaHQ9IjEyOCI+CiAgPGRlZnM+CiAgICA8bGluZWFyR3JhZGllbnQgaWQ9Imh1bGwtZ3JhZCIgeDE9IjAiIHkxPSIwIiB4Mj0iMSIgeTI9IjAiPgogICAgICA8c3RvcCBvZmZzZXQ9IjAlIiBzdG9wLWNvbG9yPSIjYTg0YTIyIi8+CiAgICAgIDxzdG9wIG9mZnNldD0iMTAwJSIgc3RvcC1jb2xvcj0iI2M0NWEyYyIvPgogICAgPC9saW5lYXJHcmFkaWVudD4KICA8L2RlZnM+CiAgPCEtLSBCYXNpbiB3YWxsczogdGFwZXJlZCwgY29udGFpbmluZyBsb3dlciBodWxsIC0tPgogIDxwYXRoIGQ9Ik0xNiA1OCBMMjggNTggTDM0IDEwMiBMOTQgMTAyIEwxMDAgNTggTDExMiA1OCBMMTA2IDExMCBMMjIgMTEwIFoiIGZpbGw9IiMyZDM3NDgiLz4KICA8IS0tIEtlZWwgYmxvY2sgLS0+CiAgPHJlY3QgeD0iNTkiIHk9IjkwIiB3aWR0aD0iMTAiIGhlaWdodD0iMTIiIGZpbGw9IiM0YTU1NjgiLz4KICA8IS0tIEh1bGw6IGdyYWRpZW50IGZvciBzdWJ0bGUgZGVwdGggLS0+CiAgPHBhdGggZD0iTTM1IDMyIEw5MyAzMiBMOTMgNTggQzkzIDgwIDc3IDkwIDY0IDkwIEM1MSA5MCAzNSA4MCAzNSA1OCBaIiBmaWxsPSJ1cmwoI2h1bGwtZ3JhZCkiLz4KICA8IS0tIFdhdGVybGluZSBzdHJpcGUgYXQgYmFzaW4gd2FsbCB0b3BzIC0tPgogIDxsaW5lIHgxPSIzNiIgeTE9IjU4IiB4Mj0iOTIiIHkyPSI1OCIgc3Ryb2tlPSIjOWU0NDIwIiBzdHJva2Utd2lkdGg9IjEuNSIvPgogIDwhLS0gQXJrIGNhYmluOiBwZWFrZWQgcm9vZiwgZ29sZGVuLW9hayB0b25lIC0tPgogIDxwYXRoIGQ9Ik02NCAxMiBMODcgMjIgTDg3IDMyIEw0MSAzMiBMNDEgMjIgWiIgZmlsbD0iI2NjN2U0NSIvPgo8L3N2Zz4K"

const reviewScript = `
(() => {
	const body = document.body;
	const resources = Array.from(document.querySelectorAll('[data-resource-id]'));
	const treeButtons = Array.from(document.querySelectorAll('[data-target-resource]'));
	const viewButtons = Array.from(document.querySelectorAll('.toolbar button[data-view]'));
	const searchInput = document.querySelector('[data-tree-search]');
	const resourceIds = new Set(resources.map((resource) => resource.dataset.resourceId));
	const isEditable = (target) => {
		if (!(target instanceof HTMLElement)) {
			return false;
		}
		const tagName = target.tagName.toLowerCase();
		return target.isContentEditable || tagName === 'input' || tagName === 'textarea' || tagName === 'select';
	};

	const resourceIdFromHash = () => {
		const id = window.location.hash.slice(1);
		return resourceIds.has(id) ? id : '';
	};

	const defaultResourceId = () => {
		if (resourceIds.has(body.dataset.defaultResource)) {
			return body.dataset.defaultResource;
		}
		return resources[0].dataset.resourceId;
	};

	const updateHash = (id) => {
		if (!resourceIds.has(id) || window.location.hash.slice(1) === id) {
			return;
		}
		history.replaceState(null, '', ` + "`#${id}`" + `);
	};

	const selectResource = (id, options = {}) => {
		if (!resourceIds.has(id)) {
			return;
		}
		let activeButton = null;
		resources.forEach((resource) => {
			resource.classList.toggle('is-active', resource.dataset.resourceId === id);
		});
		treeButtons.forEach((button) => {
			const selected = button.dataset.targetResource === id;
			button.setAttribute('aria-current', selected ? 'true' : 'false');
			if (selected) {
				activeButton = button;
			}
		});
		if (activeButton) {
			activeButton.scrollIntoView({ block: 'nearest' });
		}
		if (options.updateHash) {
			updateHash(id);
		}
	};

	treeButtons.forEach((button) => {
		button.addEventListener('click', () => selectResource(button.dataset.targetResource, { updateHash: true }));
	});

	viewButtons.forEach((button) => {
		button.addEventListener('click', () => {
			body.dataset.view = button.dataset.view;
			viewButtons.forEach((candidate) => {
				candidate.setAttribute('aria-pressed', candidate === button ? 'true' : 'false');
			});
		});
	});

	const runSearch = () => {
		if (!searchInput) {
			return;
		}
		const query = searchInput.value.trim().toLowerCase();
		document.querySelectorAll('[data-tree-app]').forEach((app) => {
			let visible = false;
			app.querySelectorAll('[data-search-text]').forEach((button) => {
				const matches = !query || button.dataset.searchText.includes(query);
				button.hidden = !matches;
				visible = visible || matches;
			});
			app.hidden = !visible;
		});
	};

	const clearSearch = () => {
		if (!searchInput) {
			return;
		}
		searchInput.value = '';
		runSearch();
	};

	const clearOrBlurSearch = () => {
		if (!searchInput) {
			return;
		}
		if (searchInput.value) {
			clearSearch();
			return;
		}
		if (document.activeElement === searchInput) {
			searchInput.blur();
		}
	};

	if (searchInput) {
		searchInput.addEventListener('input', runSearch);
		searchInput.addEventListener('keydown', (event) => {
			if (event.key === 'Escape') {
				event.preventDefault();
				event.stopPropagation();
				clearOrBlurSearch();
			}
		});
		document.addEventListener('keydown', (event) => {
			if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) {
				return;
			}
			if (event.key === '/' && !isEditable(event.target)) {
				event.preventDefault();
				searchInput.focus();
				searchInput.select();
				runSearch();
				return;
			}
			if (event.key === 'Escape' && (document.activeElement === searchInput || searchInput.value)) {
				event.preventDefault();
				clearOrBlurSearch();
			}
		});
	}

	if (resources.length > 0) {
		selectResource(resourceIdFromHash() || defaultResourceId());
	}
})();
`
