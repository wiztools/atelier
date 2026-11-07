// DOM helpers: a scroll container's near-bottom test (the history list's
// load-more trigger and the transcript's follow-the-bottom check) and the
// textarea fallback for copying text where navigator.clipboard is missing.

function isNearScrollBottom(element: HTMLElement, threshold = 48): boolean {
  return element.scrollHeight - element.scrollTop - element.clientHeight < threshold;
}

function copyTextWithTextarea(text: string) {
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', 'true');
  textarea.style.position = 'fixed';
  textarea.style.left = '-9999px';
  textarea.style.top = '0';
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand('copy');
  document.body.removeChild(textarea);
}

export {isNearScrollBottom, copyTextWithTextarea};
