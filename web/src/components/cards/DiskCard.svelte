<script lang="ts">
  import Meter from '../Meter.svelte';
  import Stat from './Stat.svelte';
  import { bytes, pct } from '../../lib/format';
  import { t } from '../../lib/i18n';
  import type { Disk } from '../../lib/types';

  let { disk }: { disk: Disk } = $props();
</script>

<Stat value={pct(disk.pct)}>
  {t('card.disk.detail', { used: bytes(disk.used), total: bytes(disk.total), free: bytes(disk.avail) })}
</Stat>
<Meter value={disk.pct} label={t('card.disk.meter', { mount: disk.mount })} />
<p class="dev">{disk.device} · {disk.fstype}</p>

<style>
  .dev {
    margin: var(--space-2) 0 0;
    font-size: 12px;
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
