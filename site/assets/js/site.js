document.addEventListener("DOMContentLoaded", () => {
  const searchForm = document.querySelector(".site-search");
  if (searchForm) {
    initSearch(searchForm);
  }

  document.querySelectorAll(".nav-reference-toggle").forEach((button) => {
    initNavReferenceToggle(button);
  });

  const sidebar = document.querySelector(".site-sidebar");
  if (sidebar) {
    initSidebarPersistence(sidebar);
  }

  document.querySelectorAll(".doc-content pre").forEach((block, index) => {
    enhanceShellCommands(block);

    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy-button";
    button.textContent = "Copy";
    button.setAttribute("aria-label", `Copy code block ${index + 1}`);

    button.addEventListener("click", async () => {
      const code = block.querySelector("code");
      if (!code || !navigator.clipboard) {
        return;
      }

      await navigator.clipboard.writeText(code.innerText);
      button.textContent = "Copied";
      button.setAttribute("aria-label", `Copied code block ${index + 1}`);
      window.setTimeout(() => {
        button.textContent = "Copy";
        button.setAttribute("aria-label", `Copy code block ${index + 1}`);
      }, 1500);
    });

    block.append(button);
  });

  document.querySelectorAll(".doc-content h2[id], .doc-content h3[id]").forEach((heading) => {
    const link = document.createElement("a");
    link.className = "heading-permalink";
    link.href = `#${heading.id}`;
    link.setAttribute("aria-label", `Link to ${heading.textContent}`);
    link.textContent = "#";
    heading.append(" ", link);
  });
});

const shellCommandNames = new Set([
  "bash",
  "brew",
  "curl",
  "docker",
  "drydock",
  "git",
  "go",
  "helm",
  "hugo",
  "kind",
  "kubectl",
  "mise",
]);

function enhanceShellCommands(block) {
  const code = block.querySelector("code.language-bash, code.language-sh, code.language-zsh");
  if (!code || code.dataset.shellCommandsEnhanced === "true") {
    return;
  }

  code.dataset.shellCommandsEnhanced = "true";
  const lines = Array.from(code.children).filter((child) => child.tagName === "SPAN");
  if (lines.length === 0) {
    wrapShellCommandRanges(code, shellCommandRanges(code.textContent || ""));
    return;
  }

  lines.forEach((line) => {
    const ranges = shellCommandRanges(line.textContent || "");
    wrapShellCommandRanges(line, ranges);
  });
}

function shellCommandRanges(line) {
  const ranges = [];
  const commandPattern = /(^\s*(?:[$%]\s*)?|\|\s*|&&\s*|\|\|\s*|;\s*|\(\s*)([A-Za-z][A-Za-z0-9_-]*)\b/g;

  let match = commandPattern.exec(line);
  while (match) {
    const command = match[2];
    if (shellCommandNames.has(command)) {
      const start = match.index + match[1].length;
      ranges.push({ start, end: start + command.length });
    }
    match = commandPattern.exec(line);
  }

  return ranges;
}

function wrapShellCommandRanges(root, ranges) {
  if (ranges.length === 0) {
    return;
  }

  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes = [];
  let offset = 0;
  let node = walker.nextNode();
  while (node) {
    const length = node.nodeValue.length;
    nodes.push({ node, start: offset, end: offset + length });
    offset += length;
    node = walker.nextNode();
  }

  nodes.forEach(({ node: textNode, start, end }) => {
    const overlappingRanges = ranges
      .map((range) => ({
        start: Math.max(range.start, start) - start,
        end: Math.min(range.end, end) - start,
      }))
      .filter((range) => range.start < range.end);

    if (overlappingRanges.length === 0) {
      return;
    }

    const fragment = document.createDocumentFragment();
    let cursor = 0;
    overlappingRanges.forEach((range) => {
      if (range.start > cursor) {
        fragment.append(document.createTextNode(textNode.nodeValue.slice(cursor, range.start)));
      }

      const command = document.createElement("span");
      command.className = "shell-command";
      command.textContent = textNode.nodeValue.slice(range.start, range.end);
      fragment.append(command);
      cursor = range.end;
    });

    if (cursor < textNode.nodeValue.length) {
      fragment.append(document.createTextNode(textNode.nodeValue.slice(cursor)));
    }

    textNode.replaceWith(fragment);
  });
}

function initNavReferenceToggle(button) {
  const storageKey = "drydock.nav.reference.expanded";
  const targetID = button.getAttribute("aria-controls");
  if (!targetID) {
    return;
  }
  const target = document.getElementById(targetID);
  if (!target) {
    return;
  }

  const referenceContext = button.dataset.referenceContext === "true";
  const storedExpanded = referenceContext ? null : storageGet("localStorage", storageKey);
  if (storedExpanded !== null) {
    setNavReferenceExpanded(button, target, storedExpanded === "true");
  }

  button.addEventListener("click", () => {
    const expanded = button.getAttribute("aria-expanded") === "true";
    const nextExpanded = !expanded;
    setNavReferenceExpanded(button, target, nextExpanded);
    storageSet("localStorage", storageKey, String(nextExpanded));
  });
}

function setNavReferenceExpanded(button, target, expanded) {
  button.setAttribute("aria-expanded", expanded ? "true" : "false");
  target.hidden = !expanded;
}

function initSidebarPersistence(sidebar) {
  const storageKey = "drydock.sidebar.scrollTop";
  const storedScrollTop = Number.parseInt(storageGet("sessionStorage", storageKey) || "", 10);

  if (Number.isFinite(storedScrollTop) && storedScrollTop > 0) {
    window.requestAnimationFrame(() => {
      sidebar.scrollTop = storedScrollTop;
    });
  }

  let scheduled = false;
  sidebar.addEventListener(
    "scroll",
    () => {
      if (scheduled) {
        return;
      }
      scheduled = true;
      window.requestAnimationFrame(() => {
        scheduled = false;
        storageSet("sessionStorage", storageKey, String(Math.round(sidebar.scrollTop)));
      });
    },
    { passive: true },
  );
}

function initSearch(form) {
  const input = form.querySelector("input[type='search']");
  const panel = form.querySelector(".search-panel");
  const resultsList = form.querySelector("#site-search-results");
  const indexUrl = form.dataset.searchIndex;
  let pages = [];
  let activeIndex = -1;

  if (!input || !panel || !resultsList || !indexUrl) {
    return;
  }

  const loadIndex = async () => {
    if (pages.length > 0) {
      return pages;
    }
    const response = await fetch(indexUrl, { credentials: "same-origin" });
    if (!response.ok) {
      throw new Error(`Search index unavailable: ${response.status}`);
    }
    pages = await response.json();
    return pages;
  };

  const closeResults = () => {
    panel.hidden = true;
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
    activeIndex = -1;
  };

  const clearSearch = () => {
    input.value = "";
    resultsList.replaceChildren();
    closeResults();
  };

  const setActive = (nextIndex) => {
    const items = Array.from(resultsList.querySelectorAll("[role='option']"));
    if (items.length === 0) {
      activeIndex = -1;
      input.removeAttribute("aria-activedescendant");
      return;
    }

    activeIndex = (nextIndex + items.length) % items.length;
    items.forEach((item, index) => {
      item.setAttribute("aria-selected", index === activeIndex ? "true" : "false");
    });
    input.setAttribute("aria-activedescendant", items[activeIndex].id);
  };

  const renderResults = (matches, terms) => {
    resultsList.replaceChildren();
    matches.forEach((match, index) => {
      const page = match.page;
      const item = document.createElement("li");
      const link = document.createElement("a");
      const title = document.createElement("span");
      const snippet = document.createElement("small");
      item.id = `site-search-result-${index}`;
      item.setAttribute("role", "option");
      item.setAttribute("aria-selected", "false");
      link.href = page.url;
      appendHighlightedText(title, page.title || "", terms);
      appendHighlightedText(snippet, match.snippet || page.summary || page.section || "", terms);
      link.append(title, snippet);
      item.append(link);
      resultsList.append(item);
    });

    panel.hidden = matches.length === 0;
    input.setAttribute("aria-expanded", matches.length === 0 ? "false" : "true");
    input.removeAttribute("aria-activedescendant");
    activeIndex = -1;
  };

  const runSearch = async () => {
    const query = input.value.trim().toLowerCase();
    if (query.length < 2) {
      closeResults();
      resultsList.replaceChildren();
      return;
    }

    try {
      const terms = query.split(/\s+/).filter(Boolean);
      const loadedPages = await loadIndex();
      const matches = loadedPages
        .map((page) => {
          const title = (page.title || "").toLowerCase();
          const haystack = `${title} ${page.section || ""} ${page.summary || ""} ${page.content || ""}`.toLowerCase();
          if (!terms.every((term) => haystack.includes(term))) {
            return null;
          }
          const score = terms.reduce((total, term) => total + (title.includes(term) ? 3 : 1), 0);
          return { page, score, snippet: searchSnippet(page, terms) };
        })
        .filter(Boolean)
        .sort((left, right) => right.score - left.score || left.page.title.localeCompare(right.page.title))
        .slice(0, 8);

      renderResults(matches, terms);
    } catch {
      closeResults();
    }
  };

  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) {
      return;
    }
    if (event.key === "/" && !isEditable(event.target)) {
      event.preventDefault();
      input.focus();
      input.select();
      runSearch();
      return;
    }
    if (event.key === "Escape" && (document.activeElement === input || input.value || !panel.hidden)) {
      event.preventDefault();
      clearSearch();
    }
  });

  input.addEventListener("input", runSearch);
  input.addEventListener("focus", runSearch);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const items = Array.from(resultsList.querySelectorAll("[role='option'] a"));
    if (activeIndex >= 0 && items[activeIndex]) {
      items[activeIndex].click();
      return;
    }
    if (items[0]) {
      items[0].click();
    }
  });
  input.addEventListener("keydown", (event) => {
    const items = Array.from(resultsList.querySelectorAll("[role='option'] a"));
    if (event.key === "Escape") {
      event.preventDefault();
      clearSearch();
      return;
    }
    if (event.key === "ArrowDown" && items.length > 0) {
      event.preventDefault();
      setActive(activeIndex + 1);
      return;
    }
    if (event.key === "ArrowUp" && items.length > 0) {
      event.preventDefault();
      setActive(activeIndex - 1);
      return;
    }
    if (event.key === "Enter" && activeIndex >= 0 && items[activeIndex]) {
      event.preventDefault();
      items[activeIndex].click();
    }
  });

  document.addEventListener("click", (event) => {
    if (!form.contains(event.target)) {
      closeResults();
    }
  });
}

function appendHighlightedText(parent, value, terms) {
  const text = String(value);
  const pattern = highlightPattern(terms);
  if (!pattern) {
    parent.textContent = text;
    return;
  }

  let offset = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > offset) {
      parent.append(document.createTextNode(text.slice(offset, match.index)));
    }
    const mark = document.createElement("mark");
    mark.className = "search-match";
    mark.textContent = match[0];
    parent.append(mark);
    offset = match.index + match[0].length;
  }
  if (offset < text.length) {
    parent.append(document.createTextNode(text.slice(offset)));
  }
}

function highlightPattern(terms) {
  const uniqueTerms = Array.from(new Set(terms.filter(Boolean))).sort((left, right) => right.length - left.length);
  if (uniqueTerms.length === 0) {
    return null;
  }
  return new RegExp(uniqueTerms.map(escapeRegExp).join("|"), "gi");
}

function searchSnippet(page, terms) {
  const source = normalizeSearchText(page.content || page.summary || page.section || "");
  if (!source) {
    return page.section || "";
  }

  const lowerSource = source.toLowerCase();
  const firstIndex = terms.reduce((best, term) => {
    const index = lowerSource.indexOf(term);
    if (index < 0) {
      return best;
    }
    return best < 0 ? index : Math.min(best, index);
  }, -1);

  if (firstIndex < 0) {
    return truncateSnippet(source, 180);
  }

  const context = 84;
  const start = Math.max(0, firstIndex - context);
  const end = Math.min(source.length, firstIndex + context);
  const prefix = start > 0 ? "... " : "";
  const suffix = end < source.length ? " ..." : "";
  return `${prefix}${source.slice(start, end).trim()}${suffix}`;
}

function normalizeSearchText(value) {
  return String(value).replace(/\s+/g, " ").trim();
}

function truncateSnippet(value, length) {
  const text = normalizeSearchText(value);
  if (text.length <= length) {
    return text;
  }
  return `${text.slice(0, length).trim()} ...`;
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function isEditable(target) {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  const tagName = target.tagName.toLowerCase();
  return target.isContentEditable || tagName === "input" || tagName === "textarea" || tagName === "select";
}

function storageGet(storageName, key) {
  try {
    return window[storageName].getItem(key);
  } catch {
    return null;
  }
}

function storageSet(storageName, key, value) {
  try {
    window[storageName].setItem(key, value);
  } catch {
    // Storage can be disabled by browser privacy settings. The site still works without it.
  }
}
