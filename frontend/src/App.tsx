import { BrowserRouter, Routes, Route } from "react-router-dom"

import { LoginPage } from "@/pages/LoginPage"
import { RacesPage } from "@/pages/RacesPage"
import { RaceDetailPage } from "@/pages/RaceDetailPage"
import { WaterBackground } from "@/components/layout/WaterBackground"

function App() {
  return (
    <BrowserRouter>
      <WaterBackground />
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/" element={<RacesPage />} />
        <Route path="/races/:raceId" element={<RaceDetailPage />} />
      </Routes>
    </BrowserRouter>
  )
}

export default App
