// Sets the theme before the first paint, so the page never flashes the wrong
// one. A file of its own because the server's content policy allows no inline
// script. Keep in step with src/theme.ts.
(function () {
  var mode = 'system'
  try {
    mode = localStorage.getItem('holdfast:theme') || 'system'
  } catch (e) {
    /* storage is blocked: follow the system */
  }
  var light = mode === 'light' || (mode !== 'dark' && window.matchMedia('(prefers-color-scheme: light)').matches)
  document.documentElement.dataset.theme = light ? 'light' : 'dark'
  var meta = document.querySelector('meta[name="theme-color"]')
  if (meta) meta.setAttribute('content', light ? '#f5f7f8' : '#0e1316')
})()
