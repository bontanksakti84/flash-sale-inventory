/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        ink: '#0b1220',
        panel: '#111c2e',
        line: '#25334a',
        mint: '#5ee0b5',
        amber: '#f7b955',
      },
      boxShadow: {
        glow: '0 0 0 1px rgba(94,224,181,.08), 0 18px 60px rgba(0,0,0,.18)',
      },
    },
  },
  plugins: [],
}
