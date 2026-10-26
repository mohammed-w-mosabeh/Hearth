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
