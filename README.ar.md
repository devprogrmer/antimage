# antimage

**Languages:** [English](README.md) · [فارسی](README.fa.md) · [Русский](README.ru.md) · [简体中文](README.zh-CN.md) · **العربية**

<div dir="rtl" align="right">

مستوى تحكم ذاتي الاستضافة لإدارة أسطول من عقد VPN/الوكيل من لوحة واحدة: أدوار
متعددة للمدراء، وصول محدود النطاق، سجل تدقيق للإلحاق فقط، ومواءمة الحالة
المرغوبة عبر gRPC متبادل المصادقة.

> **الحالة: SP1 — العمود الفقري لمستوى التحكم.** يقدّم هذا الإصدار الأساس:
> المصادقة، التخويل، التدقيق، سجل العقد، التسجيل عبر mTLS، التمهيد، عقد
> المحوّل، الصحة، وقشرة واجهة المستخدم. المحوّل الوحيد الذي يُشحن هو **stub**
> يُستخدم لإثبات التقارب من طرف إلى طرف. أما محوّلات البروتوكولات الحقيقية،
> وإدارة المشتركين، ومحاسبة حركة البيانات، والحصص فهي خارج نطاق SP1 صراحةً —
> راجع [القيود المعروفة](#القيود-المعروفة).

---

## جدول المحتويات

[ما هو antimage](#ما-هو-antimage) · [البنية المعمارية](#البنية-المعمارية) · [المزايا](#المزايا) ·
[المتطلبات](#المتطلبات) · [الأنظمة المدعومة](#أنظمة-التشغيل-المدعومة) ·
[التثبيت](#التثبيت) · [التهيئة](#التهيئة) · [المنافذ](#المنافذ) ·
[TLS و mTLS](#tls-و-mtls) · [المصادقة](#المصادقة) ·
[التخويل](#التخويل) · [إضافة عقدة](#إضافة-عقدة) ·
[تنزيل الملفات التنفيذية](#تنزيل-الملفات-التنفيذية) · [نموذج الأمان](#نموذج-الأمان) ·
[CLI](#استخدام-cli) · [API](#استخدام-api) · [السجلات](#السجلات) ·
[فحوص السلامة](#فحوص-السلامة) · [معالجة المشكلات](#معالجة-المشكلات) ·
[الترقية](#إجراء-الترقية) · [النسخ الاحتياطي](#النسخ-الاحتياطي-والاستعادة) ·
[إزالة التثبيت](#إزالة-التثبيت) · [التطوير](#إعداد-بيئة-التطوير) · [الاختبارات](#الاختبارات) ·
[النشر](#النشر) · [القيود المعروفة](#القيود-المعروفة) · [الرخصة](#الرخصة)

---

## ما هو antimage

يتكوّن antimage من برنامجين وواجهة سطر أوامر:

- **`antimage-panel`** — مستوى التحكم. يقدّم واجهة برمجة التطبيقات الخاصة
  بالمشغّل وواجهة الويب عبر HTTP، إضافةً إلى مستوى تحكم gRPC تتصل به الوكلاء
  عبر mTLS.
- **`antimage-node`** — الوكيل. يعمل على كل خادم مُدار، يسجّل نفسه، ثم يحافظ
  على تدفق طويل الأمد نحو اللوحة ويوائم المضيف مع الحالة المرغوبة التي تنشرها
  اللوحة.
- **`antimage-ctl`** — الإدارة والاستعادة المحلية، ويتعامل مباشرةً مع قاعدة
  بيانات اللوحة. هذا هو طريق العودة إلى الداخل حين تتعذّر الواجهة أو يُقفل
  جميع المدراء خارجها.

القرار التصميمي المحوري هو **مواءمة الحالة المرغوبة عبر تدفق يبدأه الوكيل**،
لا استدعاء إجراءات أمري. تنشر اللوحة كيف *ينبغي* أن تبدو العقدة؛ ويقرّر الوكيل
بنفسه كيف يصل إلى ذلك ويبلّغ عمّا فعله. وهذا يعني أن العقد لا تحتاج إلى أي
منفذ وارد، وأن العقدة غير المتصلة تشفي نفسها عند عودتها، وأن انحراف التهيئة
يُكتشف بدل أن يُستبدل بصمت.

## البنية المعمارية

```
                    ┌──────────────────────────────┐
   operator ──HTTP──►  antimage-panel              │
   (browser/CLI)     │  ├─ HTTP API + embedded SPA │  :8080
                     │  ├─ gRPC control plane      │  :8443  (mTLS)
                     │  ├─ SQLite (WAL)            │
                     │  └─ private CA              │
                     └───────────▲──────────────────┘
                                 │ agent dials out; no inbound port on the node
                     ┌───────────┴──────────────────┐
                     │  antimage-node (agent)       │
                     │  ├─ enrol (one-time token)   │
                     │  ├─ control stream (mTLS)    │
                     │  └─ adapter: observe → plan  │
                     │              → apply → verify│
                     └──────────────────────────────┘
```

**المراجعات.** كل تغيير في الحالة المرغوبة لعقدة ما يمرّ عبر نقطة اختناق واحدة
تُقوننُ المستند (RFC 8785 JCS)، وتُجزّئه بـ SHA-256، وترفع `desired_revision`
بمقدار واحد بالضبط. ولا يتقدّم `applied_revision` إلا حين يبلّغ الوكيل عن
التقارب **و** تطابق التجزئة التي طبّقها التجزئةَ التي سجّلتها اللوحة. تطابق رقم
المراجعة مع اختلاف التجزئة هو خلل في التكامل، وليس تقارباً على الإطلاق.

**تخويل من طبقتين.** يمرّ كل طلب عبر بوابة صلاحيات صريحة `rbac.Check`، وكل
استعلام محدود النطاق يطبّق بشكل مستقل مسنداً نطاقياً في SQL. ونسيان أيٍّ منهما
يظهر بذاته، لأن كلاً منهما يُختبَر على حدة.

## المزايا

- تعدّد المدراء بأربعة أدوار مدمجة: `super_admin` و`admin` و`reseller` و`readonly`
- تحديد نطاق الوصول لكل عقدة مفروضاً في SQL، لا في المعالجات وحدها
- سجل تدقيق للإلحاق فقط يغطي الإجراءات المميّزة، وحالات رفض التخويل، وحالات رفض
  التحقق من الصحة
- جلسات مبهمة من جهة الخادم (وليست JWT)، فيسري الإبطال فوراً
- مصادقة ثنائية عبر TOTP مع رموز استرداد أحادية الاستخدام
- تحديد معدل محاولات الدخول وقفل الحساب
- تسجيل العقدة برمز أحادي الاستخدام وعبر CA خاصة
- الإبطال بقائمة سماح: حذف عقدة يقفل شهادتها خارجاً على الفور
- مواءمة الحالة المرغوبة مع كشف الانحراف وتقارير تطبيق لكل خطوة
- حالة حيّة للعقد عبر Server-Sent Events
- تمهيد عبر SSH مع تثبيت مفتاح المضيف، ودون حفظ بيانات الاعتماد إطلاقاً
- واجهة ويب بدعم مفروض للكتابة من اليمين إلى اليسار وللتوطين

## المتطلبات

**مضيف اللوحة**
- Linux x86-64 أو ARM64
- نحو 200 ميغابايت من القرص للملف التنفيذي وقاعدة البيانات وسجل التدقيق؛ ويزداد
  مع حجم الأسطول
- لا قاعدة بيانات خارجية ولا وسيط رسائل ولا ذاكرة تخزين مؤقت — SQLite فقط

**العقدة المُدارة**
- Debian 11/12/13 أو Ubuntu 20.04/22.04/24.04، بمعمارية x86-64 أو ARM64
- `systemd` و`curl`
- اتصال TCP صادر إلى منفذ gRPC الخاص باللوحة. **لا حاجة إلى أي منفذ وارد.**

**البناء من المصدر**
- Go 1.26 أو أحدث
- Node.js 20+ وnpm (لبناء واجهة الويب فقط)

## أنظمة التشغيل المدعومة

| المكوّن | المدعوم | جرى التحقق منه |
|---|---|---|
| `antimage-node` | Debian 11/12/13, Ubuntu 20.04/22.04/24.04 (amd64, arm64) | `install.sh` يرفض أي شيء آخر عن قصد |
| `antimage-panel` | أي Linux بالمعماريات نفسها | يُبنى تقاطعياً ويُختبر في CI |
| مضيف البناء | Linux, macOS, Windows | تعمل مجموعة الاختبارات على الثلاثة جميعاً |

يرفض `install.sh` عمداً التوزيعات غير المدعومة **رفضاً صريحاً** بدل أن يخمّن
أسماء الحزم.

## التثبيت

### الاستنساخ والبناء

```bash
git clone https://github.com/devprogrmer/antimage.git
cd antimage
```

ابنِ واجهة الويب أولاً — فاللوحة تضمّنها بداخلها:

```bash
cd web && npm ci && npm run build && cd ..
```

ثم الملفات التنفيذية:

```bash
make build
```

أو من دون `make`:

```bash
CGO_ENABLED=0 go build -trimpath -o bin/antimage-panel ./cmd/antimage-panel
CGO_ENABLED=0 go build -trimpath -o bin/antimage-node  ./cmd/antimage-node
CGO_ENABLED=0 go build -trimpath -o bin/antimage-ctl   ./cmd/antimage-ctl
```

اختيار `CGO_ENABLED=0` مقصود: مشغّل SQLite مكتوب بلغة Go خالصة، فتكون الملفات
التنفيذية ساكنة ولا تحتاج إلى libc على الجهاز الهدف.

### تشغيل اللوحة

```bash
sudo mkdir -p /var/lib/antimage && sudo chmod 700 /var/lib/antimage
sudo ./bin/antimage-panel \
  --data-dir /var/lib/antimage \
  --http :8080 \
  --grpc :8443 \
  --grpc-hosts panel.example.com
```

عند أول تشغيل تولّد اللوحة مفتاحها الرئيسي وسلطة الشهادات الخاصة بها وقاعدة
بياناتها. وتطبع بصمة CA التي ستثبّتها على العقد:

```
level=INFO msg="antimage-panel listening" http=:8080 grpc=:8443 ca_fingerprint=… grpc_cert_hosts=[panel.example.com]
```

> **يجب أن يُدرج `--grpc-hosts` الأسماء التي تتصل بها الوكلاء فعلياً.** فهي
> تصبح أسماء SAN في شهادة TLS الخاصة باللوحة. وأي عدم تطابق يُفشل مصافحة كل
> عقدة دفعةً واحدة ويبقى غير مرئي إلى أن يحاول أحد الوكلاء.

### إنشاء أول مدير

```bash
sudo ./bin/antimage-ctl --data-dir /var/lib/antimage \
  create-admin admin 'a-long-passphrase' super_admin
```

ثم افتح `http://localhost:8080` وسجّل الدخول.

### تثبيت اللوحة كخدمة

```bash
sudo cp bin/antimage-panel /usr/local/bin/
sudo useradd --system --home /var/lib/antimage --shell /usr/sbin/nologin antimage
sudo chown -R antimage:antimage /var/lib/antimage
sudo cp packaging/antimage-panel.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now antimage-panel
```

تعمل الوحدة بهوية `User=antimage` مع `NoNewPrivileges` و`ProtectSystem=strict`
و`ProtectHome` و`PrivateTmp`. **عليك أنت إنشاء ذلك المستخدم** — فالحزمة لا
تفعل ذلك نيابةً عنك.

## إضافة عقدة

### أمر التمهيد المكوّن من سطر واحد

أنشئ العقدة في الواجهة (أو عبر `antimage-ctl`)، وخذ رمز التسجيل، ثم نفّذ ما يلي
**على العقدة نفسها**:

```bash
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- \
  --panel https://panel.example.com \
  --token YOUR_ENROLMENT_TOKEN \
  --ca-fingerprint THE_PANEL_CA_FINGERPRINT
```

يتحقق `install.sh` من نظام التشغيل والمعمارية، وينزّل الوكيل وقيمة SHA-256
الخاصة به، و**يتحقق من المجموع الاختباري قبل التثبيت**، ويكتب
`/etc/antimage/node.yaml` بالصلاحية 0600، ويثبّت وحدة systemd ويشغّلها. وإعادة
التشغيل تُرقّي التثبيت في مكانه دون استهلاك رمز جديد.

تمرير `--ca-fingerprint` عبر قناة خارج النطاق هو المسار القوي. وإن أغفلته، جلب
السكربت البصمة من اللوحة عبر HTTPS — أي الثقة عند الاستخدام الأول، وهي ثقة قد
يُبطلها سجل DNS مختطَف.

> **قبل أن يعمل هذا السطر الواحد عليك نشر الملفات التنفيذية للوكيل.** راجع
> [تنزيل الملفات التنفيذية](#تنزيل-الملفات-التنفيذية).

### التثبيت اليدوي

```bash
sudo install -m 0755 antimage-node /usr/local/bin/antimage-node
sudo mkdir -p /etc/antimage /var/lib/antimage && sudo chmod 700 /var/lib/antimage
sudo tee /etc/antimage/node.yaml >/dev/null <<'YAML'
panel_url: https://panel.example.com:8443
token: YOUR_ENROLMENT_TOKEN
ca_fingerprint: THE_PANEL_CA_FINGERPRINT
state_dir: /var/lib/antimage
YAML
sudo chmod 600 /etc/antimage/node.yaml
sudo cp packaging/antimage-node.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now antimage-node
```

### التمهيد عبر SSH من اللوحة

ينفّذ `POST /api/v1/nodes/{nodeID}/bootstrap-ssh` المثبِّت عبر SSH على مرحلتين:
يعيد الاستدعاء الأول بصمة مفتاح المضيف كي يؤكّدها إنسان، ولا ينفّذ الاستدعاء
الثاني إلا مقابل ذلك المفتاح المثبَّت. **لا تُحفظ بيانات اعتماد SSH أبداً** —
لا جدول ولا عمود ولا وسم تسلسل — وتُمحى مادة المفتاح من الذاكرة قبل أن يعود
الطلب.

## التهيئة

### رايات `antimage-panel`

| الراية | النوع | الافتراضي | إلزامية | الغرض |
|---|---|---|---|---|
| `--data-dir` | مسار | `/var/lib/antimage` | لا | قاعدة البيانات والمفتاح الرئيسي والتنزيلات. يجب أن تكون صلاحيتها 0700. |
| `--http` | عنوان إصغاء | `:8080` | لا | واجهة المشغّل وواجهة المستخدم. ضع أمامها منهياً لـ TLS. |
| `--grpc` | عنوان إصغاء | `:8443` | لا | مستوى تحكم الوكلاء. يقدّم mTLS مباشرةً. |
| `--grpc-hosts` | CSV | `localhost,127.0.0.1` | **عملياً نعم** | أسماء DNS وعناوين IP التي تتصل بها الوكلاء؛ وتصبح أسماء SAN في الشهادة. القيمة الافتراضية لا تصلح إلا للاختبار المحلي. |
| `--version` | راية | — | لا | طباعة الإصدار والخروج. |

### رايات `antimage-node`

| الراية | النوع | الافتراضي | الغرض |
|---|---|---|---|
| `--config` | مسار | `/etc/antimage/node.yaml` | تهيئة الوكيل. |
| `--version` | راية | — | طباعة الإصدار والخروج. |

### `/etc/antimage/node.yaml`

راجع [`packaging/node.yaml.example`](packaging/node.yaml.example).

| المفتاح | النوع | إلزامي | الافتراضي | الغرض | ملاحظة أمنية |
|---|---|---|---|---|---|
| `panel_url` | نص | **نعم** | — | نقطة نهاية gRPC للوحة، `https://host:port` أو `host:port`. | يُرفض عند الإقلاع إن حمل مساراً أو استعلاماً أو مخططاً غير https. |
| `token` | نص | في أول تشغيل فقط | — | رمز تسجيل أحادي الاستخدام. | يُمسح من الملف بمجرد استهلاكه. أبقِ الملف بالصلاحية 0600. |
| `ca_fingerprint` | نص | **نعم** | — | قيمة SHA-256 لشهادة CA الخاصة باللوحة، بالنظام الست عشري. | **يرفض** الإقلاع أي تهيئة تخلو منه بدل الارتداد إلى مخزن الثقة في النظام. |
| `state_dir` | مسار | لا | `/var/lib/antimage` | مفتاح العقدة وشهادتها والحالة المُدارة. | يُنشأ بالصلاحية 0700؛ والمفتاح والشهادة 0600. |
| `node_id` | int | لا | — | يُكتب بعد التسجيل. | لا تضبطه يدوياً. |

### متغيرات البيئة

| المتغير | يستخدمه | الغرض | ملاحظة أمنية |
|---|---|---|---|
| `ANTIMAGE_MASTER_KEY` | اللوحة | مفتاح رئيسي بطول 32 بايت بترميز Base64، بدلاً من ملف المفتاح. | يشفّر أسرار TOTP والمفتاح الخاص لسلطة الشهادات. وقاعدة بيانات مسرّبة من دون هذا المفتاح لا تكشف أياً منهما. فضّل الملف ذا الصلاحية 0600 على القرص ما لم تكن منصتك تحقن الأسرار عبر البيئة. |
| `ANTIMAGE_DEV_PROXY` | اللوحة | توجيه طلبات الواجهة إلى خادم تطوير Vite. | **للتطوير فقط.** لا تضبطه أبداً في بيئة الإنتاج. |

## المنافذ

| المنفذ | المكوّن | البروتوكول | مدى الانكشاف |
|---|---|---|---|
| 8080 | اللوحة | HTTP | المشغّلون. أنهِ TLS أمامه. |
| 8443 | اللوحة | gRPC فوق mTLS | العقد. يجب أن يكون قابلاً للوصول من كل عقدة مُدارة. |
| — | العقدة | لا شيء | الوكيل هو من يتصل خارجاً. **لا منفذ وارد.** |

## TLS و mTLS

مستوى التحكم متبادل المصادقة من طرف إلى طرف.

### نموذج الثقة

تدير اللوحة **سلطة شهادات خاصة بها**، تُنشأ عند أول تشغيل وتُخزَّن مشفّرة تحت
المفتاح الرئيسي. وهي ليست سلطة شهادات ضمن البنية العامة لمفاتيح الويب، ولا
تصدر سوى:

- **شهادة خادم** واحدة لمُصغي gRPC في اللوحة، بأسماء SAN المأخوذة من
  `--grpc-hosts`، صالحة 90 يوماً، ويُعاد إصدارها عند كل تشغيل للوحة؛
- **شهادة عميل واحدة لكل عقدة**، `CN = <node id>`، صالحة سنة واحدة.

### مواضع الشهادات

| ما هو | أين | الصلاحية |
|---|---|---|
| المفتاح الرئيسي | `<data-dir>/master.key` (أو `ANTIMAGE_MASTER_KEY`) | 0600 |
| شهادة CA + المفتاح المختوم | جدول `panel_ca` في `<data-dir>/antimage.db` | — |
| شهادة خادم اللوحة | في الذاكرة، ويُعاد إصدارها عند كل تشغيل | — |
| المفتاح الخاص للعقدة | `<state-dir>/node.key` | 0600 |
| شهادة العقدة | `<state-dir>/node.crt` | 0600 |
| شهادة CA المثبَّتة للوحة | `<state-dir>/panel-ca.crt` | 0600 |

### كيف يجري التسجيل

1. يولّد الوكيل زوج مفاتيحه محلياً. **لا يغادر المفتاح الخاص العقدةَ أبداً ولا
   تراه اللوحة قط.**
2. يتصل باللوحة ويتحقق من أن السلسلة المقدَّمة تحتوي شهادةً تطابق قيمة SHA-256
   الخاصة بها `ca_fingerprint`. وسجل DNS المختطَف لا يجدي شيئاً.
3. يرسل الرمز أحادي الاستخدام وطلب توقيع شهادة CSR.
4. تتحقق اللوحة من أن الرمز غير مستهلَك وغير منتهٍ ومرتبط بتلك العقدة تحديداً،
   ثم توقّع شهادة عميل، وتسجّل بصمتها، وتحرق الرمز.
5. وكل ما يلي ذلك يجري عبر mTLS.

تقدّم اللوحة `[leaf, CA]` بدل الشهادة الطرفية وحدها، وذلك تحديداً كي يتمكّن
الوكيل أثناء تسجيله — وهو لا يملك بعد أي ملف CA — من العثور على بصمته المثبَّتة
داخل السلسلة.

### التحقق والإبطال

يستخدم المُصغي `VerifyClientCertIfGiven` **لا** `RequireAndVerifyClientCert`:
فالتسجيل يحدث بالضرورة قبل أن تملك العقدة أي شهادة. وتفرض خدمة التحكم هذا
الشرط على مستوى كل استدعاء RPC، وتقارن إضافةً إلى ذلك البصمةَ المقدَّمة مع
`nodes.cert_fingerprint`.

**الإبطال قائمة سماح، لا قائمة إبطال CRL.** فاللوحة هي المدقّق الوحيد، ومن ثمّ
يزيل حذف العقدة بصمتَها ويقفلها خارجاً عند الاتصال التالي. فلا توجد قائمة CRL
تُوزَّع ولا خادم OCSP يُشغَّل.

### الانتهاء والتدوير

| الشهادة | مدة الصلاحية | التدوير |
|---|---|---|
| CA | 10 سنوات | يدوي. واستبدالها يستلزم إعادة تسجيل الأسطول كله. |
| خادم اللوحة | 90 يوماً | تلقائي — يُعاد إصدارها عند كل تشغيل للوحة. أعد تشغيل اللوحة مرة كل 90 يوماً على الأقل. |
| عميل العقدة | سنة واحدة | **ليس تلقائياً بعد — راجع [القيود المعروفة](#القيود-المعروفة).** |

### أوامر التحقق

جلب البصمة التي ينبغي للمشغّلين تثبيتها:

```bash
curl -fsS https://panel.example.com/api/v1/ca-fingerprint
```

معاينة ما يقدّمه مُصغي gRPC:

```bash
openssl s_client -connect panel.example.com:8443 -showcerts </dev/null 2>/dev/null | openssl x509 -noout -text
```

تأكيد شهادة العقدة نفسها:

```bash
sudo openssl x509 -in /var/lib/antimage/node.crt -noout -subject -dates
```

## المصادقة

- تُجزّأ كلمات المرور بخوارزمية **argon2id** ‏(m=64 MB, t=3, p=4).
- الجلسات **رموز مبهمة من جهة الخادم**، لا JWT، فيسري الإبطال فوراً. ولا يُخزَّن
  سوى قيمة SHA-256 للرمز.
- ملفات تعريف الارتباط موسومة بـ `HttpOnly` و`Secure` و`SameSite=Strict`.
- **مهلة الخمول 4 ساعات؛ ومدة الحياة المطلقة 7 أيام.** النشاط يمدّد نافذة
  الخمول؛ ولا شيء يمدّد المهلة المطلقة.
- تُحدَّد معدلات محاولات الدخول الفاشلة لكل حساب ولكل عنوان IP، ويُقفل الحساب
  بعد 5 محاولات فاشلة. واسم المستخدم المجهول يكلّف الوقت نفسه الذي يكلّفه
  المعروف، فلا يكشف توقيت الاستجابة ما إذا كان الحساب موجوداً.
- **TOTP** اختياري لكل مدير على حدة. وبمجرد تسجيله يصبح إدخال رمز صحيح إلزامياً،
  وكل مسار يتعذّر على اللوحة التحقق منه **يرفض** بدل أن يسمح بالدخول بكلمة
  المرور وحدها.
- تُصدَر عشرة **رموز استرداد أحادية الاستخدام** عند التأكيد وتُعرض مرة واحدة.

تسجيل عامل ثانٍ:

```bash
# returns {"secret":"…","provisioning_uri":"otpauth://…"}
curl -X POST https://panel.example.com/api/v1/auth/totp/enrol -b cookies.txt
# confirm with a code from your authenticator; returns the recovery codes once
curl -X POST https://panel.example.com/api/v1/auth/totp/confirm -b cookies.txt \
  -d '{"totp":"123456"}'
```

## التخويل

أربعة أدوار مدمجة:

| الدور | قراءة العقد | كتابة العقد | التسجيل | كتابة الخدمات | قراءة التدقيق | الجلسات |
|---|---|---|---|---|---|---|
| `super_admin` | ✅ الكل | ✅ | ✅ | ✅ | ✅ | ✅ |
| `admin` | ✅ ضمن نطاقه | ✅ | ✅ | ✅ | ✅ | جلساته |
| `reseller` | ✅ ضمن نطاقه | — | — | ✅ | — | جلساته |
| `readonly` | ✅ ضمن نطاقه | — | — | — | — | جلساته |

يُفرض التخويل مرتين، وبشكل مستقل في كل مرة:

1. **بوابة الصلاحيات** — يستدعي كل معالج `rbac.Check` قبل أن ينجز أي عمل.
2. **المسند النطاقي في SQL** — يرشّح كل استعلام محدود النطاق بحسب قائمة السماح
   الخاصة بالمستدعي، فحتى المعالج الذي نسي فحصه يظل عاجزاً عن قراءة عقد مدير
   آخر.

وتُكتب حالات الرفض في سجل التدقيق مع الصلاحية المطلوبة والطريقة والمسار.

## تنزيل الملفات التنفيذية

يجلب `install.sh` الوكيل من اللوحة. انشر الملفات التنفيذية بوضعها في
`<data-dir>/downloads`:

```bash
sudo mkdir -p /var/lib/antimage/downloads
sudo cp antimage-node-linux-amd64 /var/lib/antimage/downloads/
sha256sum antimage-node-linux-amd64 | awk '{print $1}' \
  | sudo tee /var/lib/antimage/downloads/antimage-node-linux-amd64.sha256
sudo chown -R antimage:antimage /var/lib/antimage/downloads
```

لا تُقدَّم سوى هذه الأسماء الأربعة، وهذه القائمة **قائمة سماح، لا أداة تنقية**:

- `antimage-node-linux-amd64` و`.sha256`
- `antimage-node-linux-arm64` و`.sha256`

وأي شيء آخر يعيد 404، بما في ذلك ملفات موجودة فعلاً داخل المجلد. ونقطة النهاية
هذه بلا مصادقة عن قصد — فالملف التنفيذي ليس سراً، والذي يخوّل الانضمام هو رمز
التسجيل.

## نموذج الأمان

| الخاصية | كيف |
|---|---|
| مفاتيح العقد | تُولَّد على العقدة؛ ولا ترى اللوحة أي مفتاح خاص قط. |
| انتحال هوية اللوحة | تثبّت الوكلاء بصمة CA؛ وسجل DNS المختطَف لا يجدي شيئاً. |
| الإبطال | قائمة سماح على `cert_fingerprint`؛ وحذف العقدة يقفلها خارجاً فوراً. |
| الأسرار في حالة السكون | تُختم أسرار TOTP ومفتاح CA بخوارزمية AES-256-GCM تحت مفتاح رئيسي يُحفظ **خارج** قاعدة البيانات. |
| بيانات اعتماد SSH | لا تُحفظ أبداً. لا جدول ولا عمود ولا وسم تسلسل؛ وتُمحى من الذاكرة بعد الاستخدام. |
| رموز التسجيل | أحادية الاستخدام، تنتهي خلال 30 دقيقة، تُخزَّن مجزّأة، وتُحرق عند الاستخدام، وتُحجب من سجلات التدقيق. |
| اجتياز المسارات | تستخدم التنزيلات قائمة سماح **إضافةً إلى** `os.OpenInRoot` الذي يأبى مغادرة المجلد ولو عبر الروابط الرمزية. |
| تكامل التدقيق | للإلحاق فقط؛ ولا يملك `audit_log` مفتاحاً أجنبياً نحو `nodes`، فلا يستطيع حذف عقدة أن يمحو أثرها هي. |
| الانحراف | تُحسب مجاميع اختبارية للملفات المُدارة؛ فأي تعديل يدوي يُكتشف ويُصحَّح، لا أن يُستبدل بصمت. |

أبلغ عن الثغرات وفق [SECURITY.md](SECURITY.md).

## استخدام CLI

```
antimage-ctl [--data-dir DIR] <command> [arguments]

  create-admin   USERNAME PASSWORD ROLE   create an admin
  reset-password USERNAME PASSWORD        set a new password, revoke their sessions
  list-admins                             list admins with roles and status
  enroll-token   NODE_ID                  print a single-use enrolment token
  backup         DEST.db                  write a consistent database copy
  version                                 print the version
```

يمسح `reset-password` أيضاً سجل محاولات الدخول الفاشلة للحساب، حتى لا تبقى
المحاولات التي أقفلت مشغّلاً خارجاً حائلةً دون دخوله بعد ذلك.

## استخدام API

جميع مسارات واجهة البرمجة تقع تحت `/api/v1`. والمصادقة تتم بملف تعريف ارتباط
للجلسة يأتي من `POST /auth/login`.

```bash
# sign in
curl -c cookies.txt -X POST https://panel.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"…","totp":"123456"}'

# create a node, then mint a bootstrap command
curl -b cookies.txt -X POST https://panel.example.com/api/v1/nodes \
  -H 'Content-Type: application/json' \
  -d '{"name":"de-1","address":"203.0.113.10"}'

curl -b cookies.txt -X POST https://panel.example.com/api/v1/nodes/1/enroll-token
```

| الطريقة | المسار | الغرض |
|---|---|---|
| POST | `/auth/login` `/auth/logout` | دورة حياة الجلسة |
| GET | `/auth/me` | الفاعل الحالي وصلاحياته |
| POST | `/auth/totp/enrol` `/auth/totp/confirm` `/auth/totp/disable` | العامل الثاني |
| GET/POST | `/nodes` | سرد العقد وإنشاؤها |
| GET/DELETE | `/nodes/{id}` | تفاصيل العقدة؛ والحذف يقفل العقدة خارجاً |
| POST | `/nodes/{id}/enroll-token` | إصدار رمز أحادي الاستخدام وأمر تمهيد |
| POST | `/nodes/{id}/bootstrap-ssh` | تمهيد SSH على مرحلتين |
| GET | `/nodes/{id}/revisions` `/nodes/{id}/apply-runs` | السجل التاريخي ونتائج التطبيق لكل خطوة |
| POST | `/nodes/{id}/services` | إنشاء خدمة (يرفع رقم المراجعة) |
| PUT/DELETE | `/services/{id}` | تحديث خدمة أو إزالتها |
| GET | `/audit` `/sessions` | سجل التدقيق؛ وجلساتك أنت |
| DELETE | `/sessions/{id}` | إبطال إحدى جلساتك |
| GET | `/events` | حالة حيّة للعقد (SSE) |
| GET | `/ca-fingerprint` | مرساة الثقة العامة (بلا مصادقة) |

تستخدم الأخطاء غلافاً واحداً: `{"error":{"code":"…","message":"…"}}`. ويحمل كل
رد ترويسة `X-Request-ID` التي تظهر أيضاً في سجل التدقيق.

## السجلات

سجلات بنيوية على stderr عبر `log/slog` في Go. وتحت systemd:

```bash
sudo journalctl -u antimage-panel -f
sudo journalctl -u antimage-node -f
```

تذهب الأحداث التشغيلية إلى **سجل التدقيق** لا إلى stdout، ويمكن الاستعلام عنها
من `GET /api/v1/audit`. ولا تُسجَّل الأسرار أبداً: فرموز التسجيل تُحجب من مخرجات
التمهيد، وأسرار TOTP ورموز الاسترداد لا تدخل سجل تدقيق قط.

## فحوص السلامة

العرض الحيّ هو `GET /api/v1/events` ‏(SSE)، الذي يدفع لقطة عن الحالة كل 3 ثوانٍ
و**يعيد التحقق من الجلسة عند كل نبضة**، فيُنهي تسجيلُ الخروج أو الإبطال التدفقَ
سريعاً.

قيم حالة العقدة:

| الحالة | المعنى |
|---|---|
| `pending` | أُنشئت، ولم يجرِ الاتصال بها قط |
| `enrolling` | صدر الرمز، ولم يكتمل التسجيل بعد |
| `online` | التدفق قائم وترسل heartbeat |
| `degraded` | متصلة، وآخر عملية تطبيق فشلت أو كانت جزئية |
| `integrity` | **طبّقت العقدة مستنداً لم تُصدر اللوحة تجزئته.** حقّق في الأمر. |
| `offline` | لا heartbeat طوال ثلاث فترات (90 ثانية) |
| `disabled` | مُعطَّلة إدارياً |

## معالجة المشكلات

**تفشل مصافحة كل العقد بعد نقل اللوحة.**
لم يعد `--grpc-hosts` مطابقاً لما تتصل به الوكلاء. راجع سطر سجل الإقلاع
(`grpc_cert_hosts=[…]`)، وصحّح الراية، وأعد التشغيل. فاللوحة تعيد إصدار شهادتها
عند كل تشغيل.

**‏`bad interpreter: /bin/bash^M` عند تشغيل install.sh.**
جرى سحب السكربت بنهايات أسطر CRLF. والمستودع يثبّت `*.sh` على LF عبر
`.gitattributes`؛ فأعد الاستنساخ أو نفّذ `dos2unix`.

**مدير مسجّل بـ TOTP لا يستطيع الدخول، والسجل يقول إن الصندوق مفقود.**
أُقلعت اللوحة من دون مفتاحها الرئيسي بينما توجد أسرار مشفّرة. وهذا سلوك
fail-closed مقصود — استعِد `master.key` أو `ANTIMAGE_MASTER_KEY`. ولا تحذف
المفتاح: فأسرار TOTP ومفتاح CA لا يمكن استرجاعها من دونه.

**عقدة عالقة في الحالة `integrity`.**
المستند الذي طبّقته يُجزَّأ إلى قيمة لم تُصدرها اللوحة قط. افحص
`GET /api/v1/nodes/{id}/apply-runs`. والحالة لاصقة عن قصد — فلن تزيلها نبضة
heartbeat.

**يفشل التمهيد عند خطوة التنزيل.**
لم تُنشر أي ملفات تنفيذية. راجع
[تنزيل الملفات التنفيذية](#تنزيل-الملفات-التنفيذية).

**‏`cannot VACUUM from within a transaction` أثناء النسخ الاحتياطي.**
جرى إصلاحه في هذا الإصدار. رقِّ `antimage-ctl`.

## إجراء الترقية

```bash
# 1. Back up first — this is consistent and safe while the panel runs.
sudo antimage-ctl --data-dir /var/lib/antimage backup /var/backups/antimage-$(date +%F).db

# 2. Replace the panel binary and restart. Migrations run automatically.
sudo systemctl stop antimage-panel
sudo cp antimage-panel /usr/local/bin/
sudo systemctl start antimage-panel

# 3. Publish the new agent binaries.
sudo cp antimage-node-linux-amd64 /var/lib/antimage/downloads/
sha256sum antimage-node-linux-amd64 | awk '{print $1}' \
  | sudo tee /var/lib/antimage/downloads/antimage-node-linux-amd64.sha256

# 4. Upgrade a node in place — re-running is idempotent and consumes no token.
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- \
  --panel https://panel.example.com --token '' \
  --ca-fingerprint THE_PANEL_CA_FINGERPRINT
```

ترحيلات قاعدة البيانات أمامية الاتجاه فقط وتُنفَّذ عند الإقلاع. **تراجَع
بالاستعادة من نسخة احتياطية، لا بخفض إصدار الملف التنفيذي.**

## النسخ الاحتياطي والاستعادة

```bash
sudo antimage-ctl --data-dir /var/lib/antimage backup /var/backups/antimage.db
sudo cp /var/lib/antimage/master.key /var/backups/master.key   # 0600, store separately
```

يستخدم `backup` أمر `VACUUM INTO` في SQLite، فينتج نسخة متسقة بينما تواصل
اللوحة عملها. وهو يأبى الكتابة فوق ملف موجود.

> **قاعدة البيانات وحدها لا تكفي.** فمن دون `master.key` يتعذّر استرجاع المفتاح
> الخاص لسلطة الشهادات وكل سرّ TOTP. خذ نسخة احتياطية منه على حدة — فتخزينه
> بجوار قاعدة البيانات يُبطل السبب ذاته الذي جعله يقيم خارجها.

الاستعادة:

```bash
sudo systemctl stop antimage-panel
sudo cp /var/backups/antimage.db /var/lib/antimage/antimage.db
sudo cp /var/backups/master.key  /var/lib/antimage/master.key
sudo chown antimage:antimage /var/lib/antimage/*
sudo systemctl start antimage-panel
```

## إزالة التثبيت

على العقدة:

```bash
sudo systemctl disable --now antimage-node
sudo rm -f /etc/systemd/system/antimage-node.service /usr/local/bin/antimage-node
sudo rm -rf /etc/antimage /var/lib/antimage
sudo systemctl daemon-reload
```

واحذف العقدة من اللوحة أيضاً — فذلك يزيل بصمتها من قائمة السماح.

على مضيف اللوحة:

```bash
sudo systemctl disable --now antimage-panel
sudo rm -f /etc/systemd/system/antimage-panel.service /usr/local/bin/antimage-panel
sudo rm -rf /var/lib/antimage      # destroys the database, CA, and master key
sudo userdel antimage
```

## إعداد بيئة التطوير

```bash
git clone https://github.com/devprogrmer/antimage.git && cd antimage
go mod download
cd web && npm ci && cd ..
```

تشغيل الواجهة مع إعادة التحميل الفوري مقابل لوحة حيّة:

```bash
cd web && npm run dev          # terminal 1
ANTIMAGE_DEV_PROXY=http://localhost:5173 go run ./cmd/antimage-panel --data-dir ./tmp   # terminal 2
```

تتطلب إعادة توليد شيفرة protobuf أداة `buf` مثبَّتة **داخل GOPATH الخاص بك، لا
على مستوى النظام كله**:

```bash
go install github.com/bufbuild/buf/cmd/buf@latest
PATH="$PATH:$(go env GOPATH)/bin" make proto
```

## الاختبارات

```bash
make test              # unit + integration, with -race
make e2e               # acceptance suite for the definition of done
make check-imports     # import boundaries and the SSH host-key policy
make check-rtl         # RTL and i18n gates
bash scripts/install_test.sh
cd web && npx vitest run && npm run lint
```

يستخدم `make test` الراية `-race` التي تحتاج إلى cgo. ومن دون سلسلة أدوات C
استخدم `go test ./... -count=1` مع ملاحظة أن كاشف التسابق لم يعمل.

تشغّل مجموعة اختبارات القبول لوحةً حقيقية ووكيلاً حقيقياً عبر loopback بـ mTLS
أصيل، وتغطي بنود تعريف الإنجاز الستة كلها. وهي لا تحتاج إلى Docker.

## النشر

ضع منهياً لـ TLS أمام `:8080`؛ واكشف `:8443` مباشرةً، لأن اللوحة تقدّم mTLS
هناك بنفسها ولأن المنهي سيكسر التحقق من شهادة العميل.

```
                 ┌── :443  → reverse proxy → :8080   (operators, HTTPS)
  panel host ────┤
                 └── :8443 → antimage-panel          (nodes, mTLS, direct)
```

قائمة التحقق: أنشئ المستخدم `antimage`؛ واضبط `/var/lib/antimage` على 0700؛
وخذ نسخة احتياطية من `master.key` على حدة؛ واضبط `--grpc-hosts` على الاسم
العام؛ وانشر الملفات التنفيذية للوكيل؛ وأعد التشغيل مرة كل 90 يوماً على الأقل
كي يُعاد إصدار شهادة الخادم.

## القيود المعروفة

هذه قيود حقيقية ومقصودة في SP1:

- **لا يُشحن سوى المحوّل stub.** يثبت التقارب وكشف الانحراف والتبليغ من طرف إلى
  طرف، لكنه لا يدير أي بروتوكول حقيقي. أما المحوّلات الحقيقية فهي مشروع فرعي
  لاحق.
- **لا إدارة للمشتركين ولا محاسبة لحركة البيانات ولا حصص ولا روابط اشتراك.**
  خارج نطاق SP1.
- **التجديد التلقائي لشهادة العقدة غير مُنفَّذ.** الشهادات تدوم سنة؛ والتجديد
  عند منتصف المدة مصمَّم لكنه لم يُبنَ.
- **لا توجد واجهة لتسجيل TOTP.** نقاط النهاية تعمل؛ لكن التطبيق أحادي الصفحة لا
  يملك شاشة لها بعد.
- **لا يوجد إعداد عام باسم «اشتراط TOTP لـ `super_admin`».** أُجّل عن قصد: فأي
  سياسة تمنع دخول المدراء العامّين الذين لم يسجّلوا TOTP قد تقفل مشغّلاً خارجاً
  تماماً، وهي تحتاج أولاً إلى مخرج طوارئ مصمَّم بعناية.
- **قيم التعدادات القادمة من الخادم تُعرض من دون ترجمة** (`converged` و`ok`
  و`restart`). فهي بيانات، لا نصوص واجهة.
- **رموز TOTP ليست أحادية الاستخدام.** يظل الرمز صالحاً طوال نافذة الانحراف
  ±30 ثانية.
- **عرض التدقيق غير مرشَّح بالنطاق.** فحامل صلاحية `audit:read` يرى كل الصفوف،
  بما فيها عناوين IP التي دخل منها مدراء آخرون. و`reseller` لا يملك تلك
  الصلاحية.
- **لا توجد نقطة نهاية للمقاييس** (لا Prometheus ولا غيرها).
- **الترحيلات التنازلية غير مختبَرة.** تراجَع بالاستعادة من نسخة احتياطية.

## الرخصة

**لم تُعلَن أي رخصة بعد.** وبحسب مواصفات المشروع، اختيار الرخصة عائد إلى القائم
على المشروع وهو شرط مسبق لأي إصدار عام؛ ويبقى المستودع خاصاً حتى ذلك الحين. وما
دام ملف `LICENSE` غير موجود، فلا يُمنح أي إذن بالاستخدام أو النسخ أو التعديل أو
التوزيع.

أما المشاريع المرجعية التي أثّرت في السلوك الوظيفي لـ antimage — 3x-ui وRebecca
وPasarGuard وvpn-ui وopenvpn_webpanel_manager وL2tp-Gui-Panel — فلم تقدّم **أي
شيفرة أو أصول أو مخطط قاعدة بيانات أو توثيق**. وكل تنفيذ هنا أصيل.

</div>
