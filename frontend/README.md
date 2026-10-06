# frontend

This template should help get you started developing with Vue 3 in Vite.

## Recommended IDE Setup

[VSCode](https://code.visualstudio.com/) + [Volar](https://marketplace.visualstudio.com/items?itemName=Vue.volar) (and disable Vetur).

## Customize configuration

See [Vite Configuration Reference](https://vitejs.dev/config/).

## Project Setup

```sh
npm install
```

### Compile and Hot-Reload for Development

```sh
npm run dev
```

The development server binds to `127.0.0.1` and proxies `/api` to
`http://127.0.0.1:80`, matching the backend's default HTTP listener. Set the
`CACAO_DEV_API_TARGET` environment variable before `npm run dev` to use a
different local backend URL. Use a server you control: login passwords and
session cookies are sent to this target.
Deploy the production build with the Cacao backend for public access.

### Compile and Minify for Production

```sh
npm run build
```

### Lint with [ESLint](https://eslint.org/)

```sh
npm run lint
```
