/**
 * サービスごとの印。**どのサービスかは名前より形で分かる。**
 * 知らないIDが来ても崩れないよう、既定の形を持たせておく。
 *
 * 設定画面と入力欄の両方で使う。**2か所に描き直さない**（片方だけ直すと、
 * 同じサービスが画面によって違う形で出る）。
 */
export function ToolIcon({ id }: { id: string }) {
  if (id === "discord") {
    return (
      <svg className="tool-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path
          fill="#5865F2"
          d="M19.3 5.3A16.9 16.9 0 0 0 15.1 4l-.2.4a12.6 12.6 0 0 1 3.7 1.9 15.7 15.7 0 0 0-13.2 0A12.6 12.6 0 0 1 9.1 4.4L8.9 4a16.9 16.9 0 0 0-4.2 1.3C2.1 9.3 1.4 13.1 1.8 16.9a17 17 0 0 0 5.1 2.6l1.1-1.7a11 11 0 0 1-1.7-.8l.4-.3a12.1 12.1 0 0 0 10.6 0l.4.3c-.5.3-1.1.6-1.7.8l1.1 1.7a17 17 0 0 0 5.1-2.6c.5-4.4-.7-8.2-2.9-11.6ZM8.5 14.7c-1 0-1.9-.9-1.9-2.1s.8-2.1 1.9-2.1 1.9 1 1.9 2.1-.8 2.1-1.9 2.1Zm7 0c-1 0-1.9-.9-1.9-2.1s.8-2.1 1.9-2.1 1.9 1 1.9 2.1-.8 2.1-1.9 2.1Z"
        />
      </svg>
    );
  }
  if (id === "calendar") {
    // Googleカレンダーの日めくり。**日付を書く。**
    // 以前は赤い四角を置いていたが、ただの色の塊にしか見えなかった
    return (
      <svg className="tool-icon" viewBox="0 0 24 24" aria-hidden="true">
        <rect x="3.5" y="4.5" width="17" height="16" rx="2.5" fill="#fff" stroke="#4285F4" strokeWidth="1.6" />
        <path d="M3.5 9.5h17" stroke="#4285F4" strokeWidth="1.6" />
        <path d="M8 3v3M16 3v3" stroke="#4285F4" strokeWidth="1.8" strokeLinecap="round" />
        <text
          x="12" y="17.8" textAnchor="middle" fill="#4285F4"
          fontSize="7.5" fontWeight="700" fontFamily="Helvetica, Arial, sans-serif"
        >
          31
        </text>
      </svg>
    );
  }
  if (id === "drive") {
    // ⚠️ **公式ロゴの形をそのまま使う。** 以前は24×24へ目分量で書いた三角で、
    // 面の向きも色の並びも実物と違っていた（2026-09-13にスマホで指摘）。
    // 比率が合わないと、小さく出したときに別のサービスに見える
    return (
      <svg className="tool-icon" viewBox="0 0 87.3 78" aria-hidden="true">
        <path fill="#0066da" d="m6.6 66.85 3.85 6.65c.8 1.4 1.95 2.5 3.3 3.3l13.75-23.8h-27.5c0 1.55.4 3.1 1.2 4.5z" />
        <path fill="#00ac47" d="m43.65 25-13.75-23.8c-1.35.8-2.5 1.9-3.3 3.3l-25.4 44a9.06 9.06 0 0 0-1.2 4.5h27.5z" />
        <path fill="#ea4335" d="m73.55 76.8c1.35-.8 2.5-1.9 3.3-3.3l1.6-2.75 7.65-13.25c.8-1.4 1.2-2.95 1.2-4.5h-27.502l5.852 11.5z" />
        <path fill="#00832d" d="m43.65 25 13.75-23.8c-1.35-.8-2.9-1.2-4.5-1.2h-18.5c-1.6 0-3.15.45-4.5 1.2z" />
        <path fill="#2684fc" d="m59.8 53h-32.3l-13.75 23.8c1.35.8 2.9 1.2 4.5 1.2h50.8c1.6 0 3.15-.45 4.5-1.2z" />
        <path fill="#ffba00" d="m73.4 26.5-12.7-22c-.8-1.4-1.95-2.5-3.3-3.3l-13.75 23.8 16.15 28h27.45c0-1.55-.4-3.1-1.2-4.5z" />
      </svg>
    );
  }
  return (
    <svg className="tool-icon" viewBox="0 0 24 24" aria-hidden="true">
      <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="1.6" />
    </svg>
  );
}
