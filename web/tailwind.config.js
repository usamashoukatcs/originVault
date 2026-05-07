/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        brand: {
          50:  '#f0f4ff',
          100: '#dce7ff',
          500: '#4f6ef7',
          600: '#3a55e0',
          700: '#2c42c4',
          900: '#1a2870',
        },
      },
    },
  },
  plugins: [],
}
