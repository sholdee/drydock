document.addEventListener("DOMContentLoaded", () => {
  const searchForm = document.querySelector(".site-search");
  if (searchForm) {
    initSearch(searchForm);
  }

  document.querySelectorAll(".doc-content pre").forEach((block, index) => {
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

  const renderResults = (matches) => {
    resultsList.replaceChildren();
    matches.forEach((page, index) => {
      const item = document.createElement("li");
      const link = document.createElement("a");
      item.id = `site-search-result-${index}`;
      item.setAttribute("role", "option");
      item.setAttribute("aria-selected", "false");
      link.href = page.url;
      link.innerHTML = `<span>${escapeHTML(page.title)}</span><small>${escapeHTML(page.summary || page.section || "")}</small>`;
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
          return { page, score };
        })
        .filter(Boolean)
        .sort((left, right) => right.score - left.score || left.page.title.localeCompare(right.page.title))
        .slice(0, 8)
        .map((match) => match.page);

      renderResults(matches);
    } catch {
      closeResults();
    }
  };

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
      closeResults();
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

function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}
