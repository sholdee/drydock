document.addEventListener("DOMContentLoaded", () => {
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
});
