import { ScoreRing } from "@/components/ScoreRing";
import { GuestInfo } from "@/components/GuestInfo";
import { IncidentForm } from "@/components/IncidentForm";
import { IdEntryForm } from "@/components/IdEntryForm";
import { DiscountDisplay } from "@/components/DiscountDisplay";
import { GuestHistory } from "@/components/GuestHistory";
import { ScoreTrends } from "@/components/ScoreTrends";
import { GuestCategory } from "@/components/GuestCategory";
import { useState } from "react";

const mockGuest = {
  name: "John Doe",
  email: "john.doe@example.com",
  status: "active" as const,
  lastStay: "March 15, 2024",
  totalStays: 12,
};

// Mock data for demonstration
const mockHistory = [
  { date: "2024-03-15", type: "incident", description: "Late checkout", scoreChange: -5 },
  { date: "2024-03-10", type: "improvement", description: "Room left in excellent condition", scoreChange: 10 },
  { date: "2024-03-05", type: "incident", description: "Noise complaint", scoreChange: -10 },
];

const mockScoreHistory = [
  { date: "Mar 1", score: 85 },
  { date: "Mar 5", score: 75 },
  { date: "Mar 10", score: 85 },
  { date: "Mar 15", score: 80 },
];

const Index = () => {
  const [showDashboard, setShowDashboard] = useState(false);
  const [guestScore, setGuestScore] = useState(85);

  const handleScoreUpdate = (newScore: number) => {
    setGuestScore(newScore);
  };

  return (
    <div className="min-h-screen bg-background p-8">
      <div className="mx-auto max-w-7xl space-y-8">
        <h1 className="mb-12 text-center text-4xl font-bold">
          {showDashboard ? "Guest Score Dashboard" : "Guest ID Entry"}
        </h1>
        
        {!showDashboard ? (
          <IdEntryForm onSubmit={() => setShowDashboard(true)} />
        ) : (
          <div className="grid grid-cols-1 gap-8 lg:grid-cols-12">
            {/* Left Column - Score and Basic Info */}
            <div className="lg:col-span-4 space-y-8">
              <div className="flex flex-col items-center">
                <ScoreRing score={guestScore} />
              </div>
              <DiscountDisplay score={guestScore} />
              <GuestCategory score={guestScore} />
            </div>
            
            {/* Middle Column - Guest Info and History */}
            <div className="lg:col-span-4 space-y-8">
              <GuestInfo guest={mockGuest} />
              <GuestHistory history={mockHistory} />
            </div>
            
            {/* Right Column - Score Trends and Incident Form */}
            <div className="lg:col-span-4 space-y-8">
              <ScoreTrends history={mockScoreHistory} />
              <IncidentForm onScoreUpdate={handleScoreUpdate} currentScore={guestScore} />
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

export default Index;