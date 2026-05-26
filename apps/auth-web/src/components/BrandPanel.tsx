export default function BrandPanel() {
  return (
    <div
      className="hidden md:flex w-[42%] shrink-0 flex-col items-center justify-center p-12 gap-6"
      style={{ background: 'linear-gradient(160deg, #0E2937 0%, #187ABF 100%)' }}
    >
      <img src="/logo.png" alt="Santos Tech" className="w-24 h-24 object-contain" />
      <div className="text-center">
        <h1 className="text-white font-bold text-3xl">Santos Tech</h1>
        <p className="text-[#49A8EB] text-base mt-2">Plataforma Educacional</p>
      </div>
      <div className="w-10 h-0.5 bg-[#0DB88F] rounded" />
    </div>
  )
}
