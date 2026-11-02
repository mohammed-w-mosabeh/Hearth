<script lang="ts">
  import EmberRail from '../components/EmberRail.svelte';
  import { apps, backups, detected, modes, sampleApp as app, secrets, shapes, volumes } from './data';
  import { iso, tr } from './tr';

  const screen = new URLSearchParams(location.search).get('screen') ?? 'apps-list';
  const tone = { ok: 'd-ok', degraded: 'd-warn', warning: 'd-warn', stopped: 'd-muted' } as const;
</script>

{#snippet appsTable(compact: boolean)}
  <table class="d-table">
    <thead>
      <tr>
        <th>{tr('State', 'الحالة')}</th><th>{tr('App', 'التطبيق')}</th><th>{tr('Services', 'الخدمات')}</th>
        {#if !compact}<th>{tr('Address', 'العنوان')}</th><th>{tr('Last backup', 'آخر نسخة')}</th><th>{tr('Attention', 'تنبيه')}</th>{/if}
      </tr>
    </thead>
    <tbody>
      {#each apps as a (a.name)}
        <tr>
          <td class={tone[a.state]} aria-label={a.state}>{shapes[a.state]}</td>
          <td><strong>{a.name}</strong></td>
          <td class="mono">{a.services}</td>
          {#if !compact}
            <td class="mono">{a.address}</td>
            <td class="mono">{a.backup}</td>
            <td>
              <span class="d-row">
                {#if a.exposed}<span class="d-chip warn">▲ {tr('Exposed', 'مكشوف')}</span>{/if}
                {#if a.update}<span class="d-chip">{tr('Update', 'تحديث')}</span>{/if}
                {#each a.flags as f (f)}<span class="d-chip crit">▲ {f}</span>{/each}
              </span>
            </td>
          {/if}
        </tr>
      {/each}
    </tbody>
  </table>
{/snippet}

{#snippet detail()}
  <dl class="d-dl">
    <dt>{tr('State', 'الحالة')}</dt><dd class="d-warn">◐ {tr('3 of 4 services healthy', '3 من 4 خدمات سليمة')}</dd>
    <dt>{tr('Address', 'العنوان')}</dt><dd class="mono">{app.address}</dd>
    <dt>{tr('Restart', 'إعادة التشغيل')}</dt><dd class="mono">unless-stopped</dd>
  </dl>
  <div class="d-danger">▲ {tr('Danger flag: gpu, opted in on 2026-10-30', `علامة خطر: gpu، فُعّلت في ${iso('2026-10-30')}`)}</div>
  <div class="d-danger">▲ {tr('Reachable from the network: 0.0.0.0:8102', 'يمكن الوصول إليه من الشبكة: 0.0.0.0:8102')}</div>
  <section>
    <h2>{tr('Logs', 'السجلات')}</h2>
    <pre class="d-pre">worker  job 41 finished in 1.2 s
worker  job 42 failed: database is busy, retrying</pre>
  </section>
  <section>
    <h2>{tr('Volumes', 'وحدات التخزين')}</h2>
    <ul class="d-list">{#each volumes as v (v.name)}<li><span class="mono">{v.name}</span> · <span class="mono">{v.size}</span></li>{/each}</ul>
  </section>
  <section>
    <h2>{tr('Backups', 'النسخ الاحتياطية')}</h2>
    <p class="d-sub">{tr('Daily at 02:30, keeping 7', `يوميًا الساعة ${iso('02:30')}، مع الاحتفاظ بـ 7`)}</p>
  </section>
  <section>
    <h2>{tr('Secrets', 'الأسرار')}</h2>
    {#each secrets as s (s)}
      <div class="d-row"><span class="mono">{s}</span><span class="mono d-muted">••••••••</span><button type="button" class="d-btn">{tr('Reveal', 'إظهار')}</button></div>
    {/each}
  </section>
  <div class="d-row">
    <button type="button" class="d-btn">{tr('Restart', 'إعادة تشغيل')}</button>
    <button type="button" class="d-btn">{tr('Update', 'تحديث')}</button>
    <button type="button" class="d-btn danger">{tr('Remove', 'إزالة')}</button>
  </div>
{/snippet}

<div class="d-shell">
  <EmberRail status="connected" onsearch={() => {}} />
  <main>
    {#if screen === 'apps-list'}
      <div class="d-page">
        <div class="d-head"><h1>{tr('Apps', 'التطبيقات')}</h1><button type="button" class="d-btn primary">{tr('Import a project', 'استيراد مشروع')}</button></div>
        <section class="d-card"><h2>{tr('Managed by Hearth', 'يديرها Hearth')}</h2>{@render appsTable(false)}</section>
        <section class="d-card">
          <h2>{tr('Detected Compose projects', 'مشاريع Compose المكتشفة')}</h2>
          <p class="d-sub">{tr('These keep working as they are. Importing is optional.', 'تستمر في العمل كما هي، والاستيراد اختياري.')}</p>
          <table class="d-table"><tbody>
            {#each detected as d (d.name)}
              <tr><td><strong>{d.name}</strong></td><td class="mono">{d.services}</td><td><button type="button" class="d-btn">{tr('Check import', 'فحص الاستيراد')}</button></td></tr>
            {/each}
          </tbody></table>
        </section>
      </div>
    {:else if screen === 'app-drawer'}
      <div class="d-split">
        <div class="d-page"><div class="d-head"><h1>{tr('Apps', 'التطبيقات')}</h1></div><section class="d-card">{@render appsTable(true)}</section></div>
        <aside class="d-drawer" aria-label={app.name}><h1>{app.name}</h1>{@render detail()}</aside>
      </div>
    {:else if screen === 'app-page'}
      <div class="d-page">
        <div class="d-head"><h1>{app.name}</h1></div>
        <section class="d-card">{@render detail()}</section>
      </div>
    {:else if screen === 'import'}
      <div class="d-page">
        <div class="d-head"><h1>{tr('Import dry-run: photos', 'تجربة الاستيراد: photos')}</h1><span class="d-chip">{tr('Nothing has changed', 'لم يتغيّر شيء')}</span></div>
        <div class="d-grid">
          <section class="d-card"><h2>{tr('Refused', 'مرفوض')}</h2>
            <ul class="d-list">
              <li class="d-crit">■ <span class="mono">build:</span> {tr('images only; pick a published image', 'الصور فقط؛ اختر صورة منشورة')}</li>
              <li class="d-crit">■ <span class="mono">/etc/localtime</span> {tr('becomes a TZ value', 'تتحول إلى قيمة TZ')}</li>
            </ul>
          </section>
          <section class="d-card"><h2>{tr('Needs your decision', 'يحتاج قرارك')}</h2>
            <ul class="d-list">
              <li class="d-warn">▲ <span class="mono">0.0.0.0:8102</span> {tr('keep on the network, or use 127.0.0.1', 'إبقاؤه على الشبكة أو استخدام 127.0.0.1')}</li>
              <li class="d-warn">▲ <span class="mono">/dev/dri</span> {tr('needs the gpu danger flag', 'يحتاج علامة الخطر gpu')}</li>
              <li class="d-warn">▲ {tr('Port 8102 is also used by sample-api', 'المنفذ 8102 يستخدمه sample-api أيضًا')}</li>
            </ul>
          </section>
          <section class="d-card"><h2>{tr('Data stays in place', 'تبقى البيانات في مكانها')}</h2>
            <ul class="d-list">{#each volumes as v (v.name)}<li><span class="mono">{v.name}</span> · {tr('reused', 'يُعاد استخدامه')}</li>{/each}</ul>
            <p class="d-sub">{tr('Project name kept. Containers are recreated once, after you confirm.', 'يبقى اسم المشروع، وتُعاد الحاويات مرة واحدة بعد تأكيدك.')}</p>
          </section>
          <section class="d-card"><h2>{tr('Converted spec', 'المواصفة المحوّلة')}</h2>
            <pre class="d-pre">{`{ "name": "photos",
  "services": { "web": {
    "image": "example/photos:2.4",
    "ports": ["127.0.0.1:8102:8080"],
    "env": { "DB_PASSWORD": "secret:DB_PASSWORD" }
} } }`}</pre>
          </section>
        </div>
        <div class="d-row"><button type="button" class="d-btn primary">{tr('Fix these first', 'أصلح هذه أولًا')}</button><button type="button" class="d-btn">{tr('Close', 'إغلاق')}</button></div>
      </div>
    {:else if screen === 'deploy'}
      <div class="d-page">
        <div class="d-head"><h1>{tr('Update photos', 'تحديث photos')}</h1></div>
        <section class="d-card"><h2>{tr('Images that change', 'الصور التي تتغيّر')}</h2>
          <table class="d-table"><tbody>
            <tr><td class="mono">web</td><td class="mono">sha256:1f3a… → sha256:9c02…</td></tr>
            <tr><td class="mono">worker</td><td class="mono">sha256:1f3a… → sha256:9c02…</td></tr>
          </tbody></table>
        </section>
        <section class="d-card"><h2>{tr('Danger flags: each needs your tick', 'علامات الخطر: كل واحدة تحتاج موافقتك')}</h2>
          <label class="d-row"><input type="checkbox" /> <span class="d-crit">▲</span> <strong class="mono">gpu</strong> {tr('Gives the container the graphics device', 'يمنح الحاوية جهاز الرسوميات')}</label>
          <label class="d-row"><input type="checkbox" /> <span class="d-crit">▲</span> <strong class="mono">bind 0.0.0.0</strong> {tr('Reachable from your network', 'يمكن الوصول إليه من شبكتك')}</label>
          <p class="d-sub">{tr('One password prompt covers this update. Each flag is written to the audit log.', 'طلب كلمة مرور واحد يغطي هذا التحديث، وتُسجّل كل علامة في سجل التدقيق.')}</p>
        </section>
        <div class="d-row"><button type="button" class="d-btn primary" disabled>{tr('Update', 'تحديث')}</button><button type="button" class="d-btn">{tr('Cancel', 'إلغاء')}</button></div>
      </div>
    {:else if screen === 'backup'}
      <div class="d-page">
        <div class="d-head"><h1>{tr('Backups: photos', 'النسخ الاحتياطية: photos')}</h1><button type="button" class="d-btn primary">{tr('Back up now', 'نسخ الآن')}</button></div>
        <section class="d-card"><h2>{tr('Archives', 'الأرشيفات')}</h2>
          <table class="d-table"><tbody>
            {#each backups as b (b.time)}
              <tr><td class={b.ok ? 'd-ok' : 'd-crit'}>{b.ok ? '●' : '■'}</td><td class="mono">{b.time}</td><td class="mono">{b.size}</td>
                <td>{#if b.ok}<button type="button" class="d-btn">{tr('Restore', 'استعادة')}</button>{:else}{tr('Not enough free space. Free some, or exclude a folder.', 'لا توجد مساحة كافية. حرّر مساحة أو استبعد مجلدًا.')}{/if}</td></tr>
            {/each}
          </tbody></table>
        </section>
        <section class="d-card"><h2>{tr('Restore preview', 'معاينة الاستعادة')}</h2>
          <div class="d-danger">▲ {tr('Overwrites photos_db and photos_cache with the copy from 2026-11-02 02:30. Newer data in them is lost.', 'يستبدل photos_db و photos_cache بنسخة 2026-11-02 02:30، وتضيع البيانات الأحدث فيهما.')}</div>
          <p class="d-sub">{tr('Excluded: /srv/photos/library, on an external disk.', 'مستبعد: /srv/photos/library على قرص خارجي.')}</p>
          <div class="d-row"><label>{tr('Type photos to continue', 'اكتب photos للمتابعة')} <input class="d-input" /></label><button type="button" class="d-btn danger" disabled>{tr('Restore', 'استعادة')}</button></div>
        </section>
      </div>
    {:else if screen === 'modes'}
      <div class="d-page">
        <div class="d-head"><h1>{tr('Modes', 'الأوضاع')}</h1><button type="button" class="d-btn primary">{tr('New mode', 'وضع جديد')}</button></div>
        <section class="d-card"><table class="d-table">
          <thead><tr><th>{tr('Mode', 'الوضع')}</th><th>{tr('Session', 'الجلسة')}</th><th>{tr('Stacks', 'المجموعات')}</th><th>{tr('Trigger', 'المشغّل')}</th><th></th></tr></thead>
          <tbody>
            {#each modes as m (m.name)}
              <tr><td><strong>{m.name}</strong>{#if m.on} <span class="d-chip">● {tr('Active', 'نشط')}</span>{/if}</td><td class="mono">{m.session}</td><td class="mono">{m.stacks}</td><td class="mono">{m.trigger}</td>
                <td><button type="button" class="d-btn">{m.on ? tr('Revert', 'تراجع') : tr('Switch', 'تبديل')}</button></td></tr>
            {/each}
          </tbody>
        </table></section>
        <p class="d-sub">{tr('Battery.toml is not valid and is ignored: line 12, unknown key "command".', 'الملف Battery.toml غير صالح ومُتجاهَل: السطر 12، مفتاح غير معروف "command".')}</p>
      </div>
    {:else if screen === 'mode-editor'}
      <div class="d-page">
        <div class="d-head"><h1>{tr('Edit mode: Work', 'تعديل الوضع: Work')}</h1></div>
        <div class="d-grid">
          <section class="d-card"><h2>{tr('Session actions', 'إجراءات الجلسة')}</h2>
            <ul class="d-list"><li>{tr('Do Not Disturb: on', 'عدم الإزعاج: مفعّل')}</li><li>{tr('Power profile: balanced', 'وضع الطاقة: متوازن')}</li><li>{tr('Open: Editor', 'فتح: Editor')}</li></ul>
          </section>
          <section class="d-card"><h2>{tr('Stack actions', 'إجراءات المجموعات')}</h2>
            <ul class="d-list"><li><span class="mono">notes</span> · {tr('start', 'تشغيل')}</li><li><span class="mono">photos</span> · {tr('stop', 'إيقاف')}</li></ul>
          </section>
          <section class="d-card"><h2>{tr('Triggers', 'المشغّلات')}</h2>
            <label class="d-row"><input type="checkbox" checked /> {tr('Weekdays at 09:00', 'أيام العمل الساعة 09:00')}</label>
            <p class="d-sub">{tr('Armed by your click. A change to the file disarms it.', 'يُفعَّل بنقرتك، وأي تغيير في الملف يلغي تفعيله.')}</p>
          </section>
        </div>
      </div>
    {:else if screen === 'mode-revert'}
      <div class="d-page">
        <div class="d-head"><h1>{tr('Revert Work', 'التراجع عن Work')}</h1></div>
        <section class="d-card"><h2>{tr('What goes back', 'ما سيعود')}</h2>
          <table class="d-table"><tbody>
            <tr><td>{tr('Do Not Disturb', 'عدم الإزعاج')}</td><td class="mono">on → off</td></tr>
            <tr><td>{tr('Power profile', 'وضع الطاقة')}</td><td class="mono">balanced → power-saver</td></tr>
            <tr><td class="mono">photos</td><td class="mono">stopped → running</td></tr>
          </tbody></table>
          <p class="d-sub">{tr('Brightness stays: you changed it by hand after switching.', 'يبقى السطوع: غيّرته يدويًا بعد التبديل.')}</p>
        </section>
        <div class="d-row"><button type="button" class="d-btn primary">{tr('Revert', 'تراجع')}</button></div>
      </div>
    {:else if screen === 'approval'}
      <div class="d-page">
        <div class="d-toast" role="status"><strong>{tr('Mode Battery is waiting', 'الوضع Battery بانتظارك')}</strong>
          <span>{tr('Stop 3 stacks? Session changes are already done.', 'إيقاف 3 مجموعات؟ تغييرات الجلسة اكتملت.')}</span>
          <div class="d-row"><button type="button" class="d-btn primary">{tr('Review', 'مراجعة')}</button></div>
        </div>
        <div class="d-head"><h1>{tr('Waiting for approval', 'بانتظار الموافقة')}</h1></div>
        <section class="d-card"><h2>{tr('Battery, triggered at 14:02 by battery below 30%', 'Battery، بدأ الساعة 14:02 لانخفاض البطارية عن 30%')}</h2>
          <ul class="d-list"><li><span class="mono">photos</span> · {tr('stop', 'إيقاف')}</li><li><span class="mono">feeds</span> · {tr('stop', 'إيقاف')}</li><li><span class="mono">monitor</span> · {tr('stop', 'إيقاف')}</li></ul>
          <p class="d-sub">{tr('Nothing runs until you approve. Your password is asked once.', 'لن يُنفَّذ شيء قبل موافقتك، وتُطلب كلمة المرور مرة واحدة.')}</p>
          <div class="d-row"><button type="button" class="d-btn primary">{tr('Approve', 'موافقة')}</button><button type="button" class="d-btn">{tr('Dismiss', 'تجاهل')}</button></div>
        </section>
      </div>
    {/if}
  </main>
</div>
