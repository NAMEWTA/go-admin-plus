<template>
  <!-- Go Gopher was designed by Renee French and is used under CC BY 3.0. -->
  <svg class="gopher" viewBox="0 0 200 270" xmlns="http://www.w3.org/2000/svg" aria-hidden="true" focusable="false">
    <defs>
      <linearGradient id="gopher-fur" gradientUnits="userSpaceOnUse" x1="100" y1="38" x2="100" y2="236">
        <stop offset="0" stop-color="#8fdfee" />
        <stop offset="1" stop-color="#55bfd6" />
      </linearGradient>
      <radialGradient id="gopher-shadow">
        <stop offset="0" stop-color="#020b17" stop-opacity=".55" />
        <stop offset="1" stop-color="#020b17" stop-opacity="0" />
      </radialGradient>
    </defs>
    <ellipse class="gopher__shadow" cx="100" cy="254" rx="66" ry="10" fill="url(#gopher-shadow)" />
    <g class="gopher__body" stroke="#0a1729" stroke-width="4.5" stroke-linejoin="round">
      <ellipse cx="64" cy="237" rx="22" ry="12" fill="#f2d3a7" transform="rotate(-12 64 237)" />
      <ellipse cx="136" cy="237" rx="22" ry="12" fill="#f2d3a7" transform="rotate(12 136 237)" />
      <ellipse cx="26" cy="158" rx="14" ry="26" fill="url(#gopher-fur)" transform="rotate(18 26 158)" />
      <ellipse cx="174" cy="158" rx="14" ry="26" fill="url(#gopher-fur)" transform="rotate(-18 174 158)" />
      <circle cx="62" cy="44" r="14" fill="url(#gopher-fur)" />
      <circle cx="138" cy="44" r="14" fill="url(#gopher-fur)" />
      <path d="M28 110a72 72 0 0 1 144 0v86a40 40 0 0 1-40 40H68a40 40 0 0 1-40-40Z" fill="url(#gopher-fur)" />
      <circle cx="70" cy="92" r="30" fill="#fff" />
      <circle cx="130" cy="92" r="30" fill="#fff" />
      <circle cx="64" cy="97" r="12.5" fill="#0a1729" stroke="none" />
      <circle cx="124" cy="97" r="12.5" fill="#0a1729" stroke="none" />
      <circle cx="59" cy="92" r="4.2" fill="#fff" stroke="none" />
      <circle cx="119" cy="92" r="4.2" fill="#fff" stroke="none" />
      <rect x="86" y="114" width="28" height="28" rx="5" fill="#fff" />
      <path d="M100 114v28" stroke-width="3.5" />
      <ellipse cx="100" cy="110" rx="18" ry="15" fill="#f2d3a7" />
      <ellipse cx="100" cy="104" rx="9.5" ry="7.5" fill="#5c3a1e" stroke="none" />
    </g>
  </svg>
</template>

<style scoped>
.gopher { display: block; width: 100%; height: auto; }
.gopher__body { animation: gopher-bob 4.4s ease-in-out infinite; }
.gopher__shadow { transform-box: view-box; transform-origin: 100px 254px; animation: gopher-shadow 4.4s ease-in-out infinite; }
@keyframes gopher-bob { 50% { transform: translateY(-5px); } }
@keyframes gopher-shadow { 50% { transform: scaleX(.93); opacity: .72; } }
@media (prefers-reduced-motion: reduce) { .gopher__body, .gopher__shadow { animation: none; } }
</style>
