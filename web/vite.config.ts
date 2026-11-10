import { fileURLToPath } from 'node:url'
import preact from '@preact/preset-vite'
import type { Plugin } from 'vite'
import { defineConfig } from 'vitest/config'

const here = (p: string) => fileURLToPath(new URL(p, import.meta.url))

const SITE = 'https://gallowforge.github.io/holdfast/'
const TITLE = 'Holdfast live demo'
const ABOUT =
  'Holdfast is a memory for AI coding agents in which every fact carries its own proof. This is its real interface on a pretend machine: change the machine and watch which facts stop being true.'

/**
 * The live demo (`make pages`): the same page, with the engine compiled for the
 * browser standing where the server would be. The swap is made here, while
 * building, so the build that goes into the binary has no demo code in it.
 */
function demoSite(): Plugin {
  const door = here('./src/api.ts')
  const demoDoor = here('./src/demo/api.ts')
  return {
    name: 'holdfast-demo-site',
    enforce: 'pre',
    async resolveId(source, importer, options) {
      const found = await this.resolve(source, importer, { ...options, skipSelf: true })
      return found && found.id.split('?')[0] === door ? demoDoor : null
    },
    transformIndexHtml: {
      order: 'pre',
      handler(html) {
        const meta = (key: 'name' | 'property', id: string, content: string) => ({ tag: 'meta', attrs: { [key]: id, content }, injectTo: 'head' as const })
        // a public page: findable, and with a card when its link is shared
        const robots = /^\s*<meta name="robots"[^>]*>\n/m
        const title = '<title>Holdfast</title>'
        if (!robots.test(html) || !html.includes(title)) throw new Error('index.html changed: the demo build no longer finds its robots meta or its title')
        return {
          html: html.replace(robots, '').replace(title, `<title>${TITLE}</title>`),
          tags: [
            meta('name', 'description', ABOUT),
            meta('property', 'og:type', 'website'),
            meta('property', 'og:title', TITLE),
            meta('property', 'og:description', ABOUT),
            meta('property', 'og:url', SITE),
            meta('property', 'og:image', SITE + 'social.png'),
            meta('name', 'twitter:card', 'summary_large_image'),
          ],
        }
      },
    },
  }
}

export default defineConfig(({ mode }) => {
  const pages = mode === 'pages'
  return {
    // the demo may be served from any path; the binary serves from the root
    base: pages ? './' : '/',
    plugins: [preact(), ...(pages ? [demoSite()] : [])],
    build: {
      outDir: pages ? '../pages' : '../internal/web/dist',
      emptyOutDir: true,
      target: 'es2022',
      // the server's content policy allows no inline data, and fonts are files
      assetsInlineLimit: 0,
      cssCodeSplit: false,
      reportCompressedSize: false,
    },
  }
})
