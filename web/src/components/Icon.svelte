<script lang="ts">
  // Small hand-drawn 24×24 stroke icons; no icon library.
  const paths: Record<string, string[]> = {
    home: ['M3 11 12 4l9 7', 'M5 10v10h14V10', 'M10 20v-6h4v6'],
    chart: ['M4 20V4', 'M4 20h16', 'M8 16l4-6 3 3 5-7'],
    grid: ['M4 4h6v6H4z', 'M14 4h6v6h-6z', 'M4 14h6v6H4z', 'M14 14h6v6h-6z'],
    gear: [
      'M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z',
      'M12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M4.9 19.1 7 17M17 7l2.1-2.1',
    ],
    box: ['M3 8l9-5 9 5v8l-9 5-9-5z', 'M3 8l9 5 9-5', 'M12 13v8'],
    package: ['M4 7h16v13H4z', 'M2 4h20v3H2z', 'M10 11h4'],
    network: ['M12 3v6', 'M5 21v-4h14v4', 'M12 9v8', 'M8 3h8v6H8z'],
    terminal: ['M3 5h18v14H3z', 'M7 10l3 2-3 2', 'M12 15h5'],
    bell: ['M6 16V11a6 6 0 0 1 12 0v5l2 2H4z', 'M10 20a2 2 0 0 0 4 0'],
    sliders: ['M4 6h10M18 6h2', 'M4 12h4M12 12h8', 'M4 18h12M20 18h0', 'M16 4v4M10 10v4M18 16v4'],
    disk: ['M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3v12c0 1.7-3.6 3-8 3s-8-1.3-8-3z', 'M4 6c0 1.7 3.6 3 8 3s8-1.3 8-3', 'M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3'],
    search: ['M10.5 4a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13z', 'M15.5 15.5 20 20'],
    flame: ['M12 3c1 4 5 5 5 10a5 5 0 0 1-10 0c0-3 2-4 2-7 1 1 2 2 3 2 0-2-1-4 0-5z'],
    // Homepage tile icons.
    globe: ['M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z', 'M3 12h18', 'M12 3c2.5 2.5 3.5 5.5 3.5 9s-1 6.5-3.5 9c-2.5-2.5-3.5-5.5-3.5-9s1-6.5 3.5-9z'],
    server: ['M4 4h16v6H4z', 'M4 14h16v6H4z', 'M8 7h.01M8 17h.01'],
    database: ['M5 6c0-1.7 3.1-3 7-3s7 1.3 7 3v12c0 1.7-3.1 3-7 3s-7-1.3-7-3z', 'M5 6c0 1.7 3.1 3 7 3s7-1.3 7-3', 'M5 12c0 1.7 3.1 3 7 3s7-1.3 7-3'],
    cloud: ['M7 18a4 4 0 0 1-.5-8 6 6 0 0 1 11.5 1.5A3.5 3.5 0 0 1 17.5 18z'],
    code: ['M9 8l-4 4 4 4', 'M15 8l4 4-4 4', 'M13 5l-2 14'],
    shield: ['M12 3l8 3v6c0 4.5-3.4 8-8 9-4.6-1-8-4.5-8-9V6z'],
    mail: ['M3 6h18v12H3z', 'M3 7l9 6 9-6'],
    book: ['M5 4h11a3 3 0 0 1 3 3v13H8a3 3 0 0 1-3-3z', 'M5 17a3 3 0 0 1 3-3h11'],
    film: ['M4 4h16v16H4z', 'M8 4v16M16 4v16', 'M4 9h4M4 15h4M16 9h4M16 15h4'],
    music: ['M9 18V5l11-2v13', 'M9 18a3 3 0 1 1-6 0 3 3 0 0 1 6 0z', 'M20 16a3 3 0 1 1-6 0 3 3 0 0 1 6 0z'],
    photo: ['M4 5h16v14H4z', 'M4 16l5-5 4 4 2-2 5 5', 'M15 9h.01'],
    camera: ['M4 8h4l2-3h4l2 3h4v11H4z', 'M12 10a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7z'],
    download: ['M12 4v11', 'M7 10l5 5 5-5', 'M5 20h14'],
    chat: ['M4 5h16v11H9l-5 4z'],
    report: ['M6 3h9l4 4v14H6z', 'M15 3v4h4', 'M9 17v-3M12 17v-6M15 17v-4'],
    calendar: ['M4 6h16v14H4z', 'M4 10h16', 'M8 3v5M16 3v5'],
    wrench: ['M14.5 4a4.5 4.5 0 0 0-4.2 6.1L4 16.4 7.6 20l6.3-6.3A4.5 4.5 0 0 0 20 9.5l-3 3-3.5-3.5 3-3a4.5 4.5 0 0 0-2-2z'],
    bolt: ['M13 3L5 14h6l-1 7 8-11h-6z'],
    gamepad: ['M7 8h10a5 5 0 0 1 4.9 6l-.6 3a2.5 2.5 0 0 1-4.3 1.2L15 16H9l-2 2.2A2.5 2.5 0 0 1 2.7 17l-.6-3A5 5 0 0 1 7 8z', 'M8 11v4M6 13h4', 'M15.5 12h.01M17.5 14h.01'],
    layers: ['M12 3l9 5-9 5-9-5z', 'M3 13l9 5 9-5', 'M3 17l9 5 9-5'],
    broom: ['M19 3l-7 7', 'M12 8l4 4', 'M9 11c-3 1-5 4-5 9h9c2-1 4-4 3-8', 'M8 20c0-2 .5-3 1.5-4', 'M12 20c0-1.5.3-2.5 1-3.5'],
    history: ['M4 12a8 8 0 1 0 2.6-5.9', 'M4 4v4h4', 'M12 8v4l3 2'],
    link: ['M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1', 'M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1'],
  };

  let { name, size = 20 }: { name: string; size?: number } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="1.75"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
>
  {#each paths[name] ?? [] as d (d)}
    <path {d} />
  {/each}
</svg>
