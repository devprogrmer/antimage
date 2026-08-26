import { MyTenancy } from "../components/MyTenancy";
import { SessionList } from "../components/SessionList";
import { TelegramLink } from "../components/TelegramLink";
import { t } from "../i18n";

/**
 * Profile holds the settings that act on the SIGNED-IN account only.
 *
 * Everything here is scoped to the session server-side and takes no admin id,
 * which is what lets a reseller manage their own integrations without holding
 * any permission over other accounts.
 */
export function Profile() {
  return (
    <div className="space-y-4">
      <h1 className="text-sm font-semibold text-foreground">{t("profile.title")}</h1>
      <SessionList />
      <div className="max-w-xl space-y-4">
        <MyTenancy />
        <TelegramLink />
      </div>
    </div>
  );
}
